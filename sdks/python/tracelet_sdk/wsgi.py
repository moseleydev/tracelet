"""WSGI middleware: one request context per HTTP request."""

from typing import Any, Callable, Dict, Optional

from . import new_traceparent, request

RouteGetter = Callable[[Dict[str, Any]], Optional[str]]


class TraceletMiddleware:
    """Wrap any WSGI app. ``route`` is called with the WSGI environ each
    time a query runs and should return the matched route pattern (for
    example ``"GET /users/<id>"``). Without it, queries are still grouped by
    request, just without a route. Raw URLs are never used, so ids and other
    values in paths don't end up in your stats."""

    def __init__(self, app: Callable, route: Optional[RouteGetter] = None):
        self.app = app
        self.route = route

    def __call__(self, environ: Dict[str, Any], start_response: Callable):
        getter = (lambda: self.route(environ)) if self.route else None
        with request(route=getter, traceparent=new_traceparent(environ.get("HTTP_TRACEPARENT"))):
            # Queries made while a streamed response is being iterated, after
            # this returns, are not tagged.
            return self.app(environ, start_response)
