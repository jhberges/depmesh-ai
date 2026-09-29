package sources

import (
	"net/http"
	"os"
	"strings"
	"sync"
)

// Repository configuration for the ecosystems whose packages do not all live
// on one public registry.
//
// Maven is the case that forces this. An organisation's own artifacts sit on
// an internal Nexus or Artifactory, and Maven Central has never heard of
// them — so vetting them against Central alone reports every in-house library
// as a package that does not exist, which is the tool's loudest possible
// alarm and exactly wrong. Gradle plugin *markers* are the same shape for a
// different reason: they are published to the Gradle Plugin Portal, not
// Central.
//
// Namespaces are what keeps this from becoming a hole. A repository that
// declares them is *authoritative* for those groupId prefixes: coordinates
// under them are resolved there and nowhere else. That is the dependency
// confusion defence — a public registry can never shadow an internal
// namespace, and a typo inside one still fails, because the internal
// repository is asked and says no.

// MavenRepo is one repository to resolve Maven coordinates against.
type MavenRepo struct {
	// URL is the repository root — the directory that groupId/artifactId
	// paths hang off, e.g. https://nexus.example.com/repository/maven-releases
	URL string `json:"url"`

	// Namespaces are groupId prefixes this repository owns, matched on dot
	// boundaries ("com.acme" covers com.acme.platform, not com.acmex). When set, the
	// repository is the only place coordinates under them are looked for, and
	// they are treated as internal: never reported to slopsquat telemetry,
	// because an in-house artifact name is not a hallucination and is nobody
	// else's business.
	//
	// When empty, the repository joins the public search chain instead, tried
	// after Maven Central. That is the form to use for a public repository
	// Central does not mirror, such as the Gradle Plugin Portal.
	Namespaces []string `json:"namespaces,omitempty"`

	// Credentials are named here and read from the environment, never written
	// here: the policy file is version-controlled, and a repository password
	// in git is a worse problem than the one this feature solves.
	UsernameEnv string `json:"username_env,omitempty"`
	PasswordEnv string `json:"password_env,omitempty"`
	// TokenEnv names a bearer token, for repositories that take one instead
	// of a username and password.
	TokenEnv string `json:"token_env,omitempty"`
}

// Config is the source layer's view of repository configuration. It is
// resolved from the policy file and applied once, at startup, by the gate.
type Config struct {
	Maven []MavenRepo `json:"maven,omitempty"`
}

// configured is process-wide because the alternative is threading a config
// through every fetcher and into the HTTP layer, where credentials are
// needed. Every surface builds exactly one gate before serving, so it is
// written once and read thereafter; the mutex is for the race detector's
// benefit and for tests that swap it.
var (
	configMu   sync.RWMutex
	configured Config
)

// Configure installs repository configuration. Called by the gate at
// startup; calling it twice replaces the previous configuration wholesale.
func Configure(c Config) {
	configMu.Lock()
	defer configMu.Unlock()
	configured = c
}

func mavenRepos() []MavenRepo {
	configMu.RLock()
	defer configMu.RUnlock()
	return configured.Maven
}

// ownsNamespace reports whether prefix covers group, matching on dot
// boundaries so that "com.acme" owns com.acme.platform without also claiming
// com.acmecorp.
func ownsNamespace(prefix, group string) bool {
	prefix, group = strings.ToLower(prefix), strings.ToLower(group)
	return group == prefix || strings.HasPrefix(group, prefix+".")
}

// mavenSearchPath returns the repositories to resolve a groupId against, and
// whether that group belongs to an internal namespace.
//
// A namespace match wins outright: the declaring repositories are the whole
// search path, and Central is not consulted. Anything else searches Central
// first and then the repositories that declared no namespace.
func mavenSearchPath(group string) (repos []MavenRepo, internal bool) {
	for _, repo := range mavenRepos() {
		for _, ns := range repo.Namespaces {
			if ownsNamespace(ns, group) {
				repos = append(repos, repo)
				internal = true
				break
			}
		}
	}
	if internal {
		return repos, true
	}
	repos = append(repos, MavenRepo{URL: mavenCentral})
	for _, repo := range mavenRepos() {
		if len(repo.Namespaces) == 0 {
			repos = append(repos, repo)
		}
	}
	return repos, false
}

// credentialsFor finds the configured repository a URL belongs to and returns
// how to authenticate to it. Matching is by URL prefix, so every path under a
// configured repository root carries its credentials — and nothing else does,
// which is what stops an internal token being sent to Maven Central.
func credentialsFor(url string) (func(*http.Request), bool) {
	for _, repo := range mavenRepos() {
		if repo.URL == "" || !underRepo(url, repo.URL) {
			continue
		}
		if token := os.Getenv(repo.TokenEnv); repo.TokenEnv != "" && token != "" {
			return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }, true
		}
		user, password := os.Getenv(repo.UsernameEnv), os.Getenv(repo.PasswordEnv)
		if repo.UsernameEnv != "" && user != "" {
			return func(r *http.Request) { r.SetBasicAuth(user, password) }, true
		}
		return nil, false
	}
	return nil, false
}

// underRepo reports whether url addresses something inside repo, matching on a
// path boundary so that a repository at /maven-releases does not also claim
// /maven-releases-staging next to it on the same host.
func underRepo(url, repo string) bool {
	repo = strings.TrimSuffix(repo, "/")
	return url == repo || strings.HasPrefix(url, repo+"/")
}
