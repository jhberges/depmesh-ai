package model

import "testing"

func TestIsPrerelease(t *testing.T) {
	for _, version := range []string{
		"1.0.0-rc1", "1.0.0-RC1", "2.0.0-alpha.3", "1.0.0-beta", "1.0-M3",
		"2.0.0.CR1", "1.0a1", "2.0b3", "0.1.0-dev", "1.0.0-canary.7",
		"3.0.0-next.2", "1.0-SNAPSHOT", "1.0.0-milestone1", "1.0.0-ea",
	} {
		if !IsPrerelease(version) {
			t.Errorf("%q not recognized as a prerelease", version)
		}
	}
	// Releases that look prerelease-ish and are not. Misreading one of these
	// would drop a real release out of a release count.
	for _, version := range []string{
		"1.0.0", "4.18.2", "1.0.0.Final", "2.31.0", "0.0.1", "1.0.0+build.7",
		"20240115", "1.2.3-4",
	} {
		if IsPrerelease(version) {
			t.Errorf("%q wrongly flagged as a prerelease", version)
		}
	}
}

func TestIsSnapshot(t *testing.T) {
	if !IsSnapshot("1.0.0-SNAPSHOT") || !IsSnapshot("2.0-snapshot") {
		t.Error("snapshot not recognized")
	}
	if IsSnapshot("1.0.0") {
		t.Error("release wrongly read as a snapshot")
	}
}

func TestIsConstraint(t *testing.T) {
	for _, version := range []string{
		"^4.18.0", "~2.31", ">=1.0.0", "[1.0,2.0)", "1.+", "1.2.+", "*",
		"latest.release", "latest", "1.0.0 || 2.0.0", "!=1.0", "1.0,2.0",
	} {
		if !IsConstraint(version) {
			t.Errorf("%q not recognized as a constraint", version)
		}
	}
	// Exact versions, including the awkward ones. A false positive here refuses
	// to vet a perfectly good coordinate.
	for _, version := range []string{
		"1.0.0", "1.0.0.Final", "2.0-20240115.RC3", "4.18.2", "1.0.0-SNAPSHOT",
		"1.0.0+build.7", "2.31.0.post1", "v1.2.3",
	} {
		if IsConstraint(version) {
			t.Errorf("%q wrongly flagged as a constraint", version)
		}
	}
}
