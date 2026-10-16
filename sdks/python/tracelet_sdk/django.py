"""Django integration. Add to MIDDLEWARE::

    MIDDLEWARE = ["tracelet_sdk.django.TraceletMiddleware", ...]

Every query Django runs during a request is tagged with the URL pattern
("GET /users/<int:pk>/"). No other setup is needed.
"""

from contextlib import ExitStack
from typing import Any, Callable

from . import comment, new_traceparent, request


def _execute_wrapper(execute: Callable, sql: str, params: Any, many: bool, context: Any) -> Any:
    # With parameters, Django's backends %-format the SQL, so the comment's
    # % signs must be doubled. Without parameters they are sent as-is.
    return execute(comment(sql, escape_percent=params is not None), params, many, context)


class TraceletMiddleware:
    def __init__(self, get_response: Callable):
        self.get_response = get_response

    def __call__(self, http_request: Any) -> Any:
        from django.db import connections

        def route():
            match = getattr(http_request, "resolver_match", None)
            pattern = getattr(match, "route", None)
            return "{} /{}".format(http_request.method, pattern) if pattern is not None else None

        incoming = http_request.META.get("HTTP_TRACEPARENT")
        with request(route=route, traceparent=new_traceparent(incoming)), ExitStack() as stack:
            for conn in connections.all():
                stack.enter_context(conn.execute_wrapper(_execute_wrapper))
            return self.get_response(http_request)
