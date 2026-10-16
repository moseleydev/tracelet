"""Tag each database query with the request that ran it.

Queries get a sqlcommenter comment (https://google.github.io/sqlcommenter/)::

    SELECT * FROM users WHERE id = %s /*route='GET%20%2Fusers%2F%3Aid',traceparent='00-…-…-01'*/

Tracelet reads these comments to group queries into requests. Nothing is
sent anywhere: the comment travels with the query to your database.

Framework integrations live in submodules: ``tracelet_sdk.wsgi``,
``tracelet_sdk.asgi``, ``tracelet_sdk.flask``, ``tracelet_sdk.django`` and
``tracelet_sdk.sqlalchemy``.
"""

import contextvars
import os
import re
from contextlib import contextmanager
from typing import Callable, Dict, Iterator, Optional, Union
from urllib.parse import quote

__all__ = [
    "RequestContext",
    "request",
    "current_request",
    "comment",
    "new_traceparent",
    "parse_traceparent",
]

Route = Union[str, Callable[[], Optional[str]], None]

_TRACEPARENT = re.compile(r"^([0-9a-f]{2})-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})$")


class RequestContext:
    """The request a query belongs to."""

    __slots__ = ("_route", "traceparent")

    def __init__(self, route: Route = None, traceparent: Optional[str] = None):
        self._route = route
        self.traceparent = traceparent if parse_traceparent(traceparent) else new_traceparent(traceparent)

    @property
    def route(self) -> Optional[str]:
        """The route pattern. Callables are evaluated now, so frameworks that
        only know the route after routing can supply it lazily."""
        route = self._route
        if callable(route):
            try:
                route = route()
            except Exception:  # never let tagging break a query
                route = None
        return route or None


_current: "contextvars.ContextVar[Optional[RequestContext]]" = contextvars.ContextVar("tracelet_request", default=None)


def parse_traceparent(header: Optional[str]) -> Optional[Dict[str, str]]:
    """Parse a W3C traceparent header, or return None if it is not valid."""
    if not isinstance(header, str):
        return None
    m = _TRACEPARENT.match(header.strip())
    if not m or m.group(1) == "ff" or set(m.group(2)) == {"0"} or set(m.group(3)) == {"0"}:
        return None
    return {"version": m.group(1), "trace_id": m.group(2), "parent_id": m.group(3), "flags": m.group(4)}


def new_traceparent(incoming: Optional[str] = None) -> str:
    """A traceparent for a new request span. An incoming header's trace id is
    kept, so Tracelet's traces line up with the rest of your tracing."""
    parent = parse_traceparent(incoming)
    trace_id = parent["trace_id"] if parent else os.urandom(16).hex()
    flags = parent["flags"] if parent else "01"
    return "00-{}-{}-{}".format(trace_id, os.urandom(8).hex(), flags)


@contextmanager
def request(route: Route = None, traceparent: Optional[str] = None) -> Iterator[RequestContext]:
    """Run a block as one request: queries inside it are tagged with it.
    Works across ``await`` and in threads that copy the context."""
    ctx = RequestContext(route, traceparent)
    token = _current.set(ctx)
    try:
        yield ctx
    finally:
        _current.reset(token)


def current_request() -> Optional[RequestContext]:
    """The current request context, or None outside a request."""
    return _current.get()


def _enc(value: str) -> str:
    # Same as JavaScript's encodeURIComponent, then sqlcommenter's \' escape.
    return quote(str(value), safe="-_.!~*'()").replace("'", "\\'")


def comment(sql: str, fields: Optional[Dict[str, Optional[str]]] = None, *, escape_percent: bool = False) -> str:
    """Append a sqlcommenter comment with ``fields`` (or the current
    request's) to ``sql``.

    Set ``escape_percent`` when the driver will %-format the SQL (psycopg,
    MySQLdb and Django with parameters): the comment's URL-encoded values
    contain ``%``, which must then be written as ``%%``.

    SQL that already contains a comment is returned unchanged, as the
    sqlcommenter spec requires.
    """
    if not isinstance(sql, str) or "*/" in sql:
        return sql
    if fields is None:
        ctx = _current.get()
        if ctx is None:
            return sql
        fields = {"route": ctx.route, "traceparent": ctx.traceparent}
    pairs = ["{}='{}'".format(_enc(k), _enc(v)) for k, v in sorted(fields.items()) if v not in (None, "")]
    if not pairs:
        return sql

    text = "/*" + ",".join(pairs) + "*/"
    if escape_percent:
        text = text.replace("%", "%%")
    trimmed = sql.rstrip()
    if trimmed.endswith(";"):
        return "{} {};".format(trimmed[:-1].rstrip(), text)
    return "{} {}".format(trimmed, text)
