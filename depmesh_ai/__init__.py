"""depmesh-ai: vet open-source dependencies before adopting them."""
from .vet import Advice, Verdict, vet

__version__ = "0.1.0"
__all__ = ["vet", "Verdict", "Advice", "__version__"]
