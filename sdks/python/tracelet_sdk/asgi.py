"""ASGI middleware: one request context per HTTP request (FastAPI, Starlette, Django ASGI)."""

from typing import Any, Callable, Dict, Optional

from . import new_traceparent, request

RouteGetter = Callable[[Dict[str, Any]], Optional[str]]


def default_route(scope: Dict[str, Any]) -> Optional[str]:
    """The route pattern FastAPI records in the scope after routing."""
    route = scope.get("route")
    path = getattr(route, "path", None) or getattr(route, "path_format", None)
    return "{} {}".format(scope.get("method", ""), path).strip() if path else None


class TraceletMiddleware:
    """Wrap any ASGI app. ``route`` is called with the ASGI scope each time a
    query runs; the default reads the route FastAPI matched."""

    def __init__(self, app: Callable, route: Optional[RouteGetter] = default_route):
        self.app = app
        self.route = route

    async def __call__(self, scope: Dict[str, Any], receive: Callable, send: Callable) -> None:
        if scope.get("type") != "http":
            await self.app(scope, receive, send)
            return
        incoming = None
        for name, value in scope.get("headers") or []:
            if name == b"traceparent":
                incoming = value.decode("latin-1")
                break
        getter = (lambda: self.route(scope)) if self.route else None
        with request(route=getter, traceparent=new_traceparent(incoming)):
            await self.app(scope, receive, send)
