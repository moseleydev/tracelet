import asyncio
import json
import os
import re
import sys
import threading
import unittest
from contextvars import copy_context

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import tracelet_sdk as sdk  # noqa: E402
from tracelet_sdk import asgi, wsgi  # noqa: E402

VECTORS = os.path.join(os.path.dirname(__file__), "..", "..", "testdata", "sqlcommenter.json")
TP = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"


def installed(module):
    try:
        __import__(module)
        return True
    except ImportError:
        return False


class CommentTest(unittest.TestCase):
    def test_shared_vectors_byte_for_byte(self):
        with open(VECTORS, encoding="utf-8") as f:
            cases = json.load(f)["cases"]
        for c in cases:
            with self.subTest(c["name"]):
                self.assertEqual(sdk.comment(c["sql"], c["fields"]), c["commented"])

    def test_uses_current_request_and_is_noop_outside(self):
        self.assertEqual(sdk.comment("SELECT 1"), "SELECT 1")
        with sdk.request(route="GET /a", traceparent=TP):
            self.assertEqual(sdk.comment("SELECT 1"), "SELECT 1 /*route='GET%20%2Fa',traceparent='{}'*/".format(TP))
        self.assertIsNone(sdk.current_request())

    def test_escape_percent_for_format_style_drivers(self):
        out = sdk.comment("SELECT * FROM t WHERE id = %s", {"route": "/a b"}, escape_percent=True)
        self.assertEqual(out, "SELECT * FROM t WHERE id = %s /*route='%%2Fa%%20b'*/")
        self.assertEqual(out % ("1",), "SELECT * FROM t WHERE id = 1 /*route='%2Fa%20b'*/")

    def test_lazy_route_errors_never_break_queries(self):
        def broken():
            raise RuntimeError("no route yet")

        with sdk.request(route=broken, traceparent=TP):
            self.assertEqual(sdk.comment("SELECT 1"), "SELECT 1 /*traceparent='{}'*/".format(TP))


class TraceparentTest(unittest.TestCase):
    def test_parse(self):
        self.assertEqual(sdk.parse_traceparent(TP)["trace_id"], "4bf92f3577b34da6a3ce929d0e0e4736")
        for bad in (None, "", "nope", "00-" + "0" * 32 + "-00f067aa0ba902b7-01", "ff" + TP[2:]):
            self.assertIsNone(sdk.parse_traceparent(bad), bad)

    def test_new_continues_incoming_trace(self):
        self.assertIsNotNone(sdk.parse_traceparent(sdk.new_traceparent()))
        cont = sdk.parse_traceparent(sdk.new_traceparent(TP))
        self.assertEqual(cont["trace_id"], "4bf92f3577b34da6a3ce929d0e0e4736")
        self.assertNotEqual(cont["parent_id"], "00f067aa0ba902b7")

    def test_invalid_traceparent_is_replaced(self):
        with sdk.request(traceparent="garbage") as ctx:
            self.assertIsNotNone(sdk.parse_traceparent(ctx.traceparent))


class ContextTest(unittest.TestCase):
    def test_asyncio_tasks_keep_separate_context(self):
        async def handle(route):
            with sdk.request(route=route):
                await asyncio.sleep(0.01)
                return route, sdk.comment("SELECT 1")

        async def main():
            return await asyncio.gather(*(handle(r) for r in ("/a", "/b", "/c")))

        for route, sql in asyncio.run(main()):
            self.assertIn("route='%2F{}'".format(route[1:]), sql)

    def test_threads_that_copy_context(self):
        seen = []
        with sdk.request(route="/t"):
            ctx = copy_context()
        t = threading.Thread(target=lambda: seen.append(ctx.run(sdk.comment, "SELECT 1")))
        t.start()
        t.join()
        self.assertIn("route='%2Ft'", seen[0])


class MiddlewareTest(unittest.TestCase):
    def test_wsgi(self):
        seen = {}

        def app(environ, start_response):
            seen["before"] = sdk.comment("SELECT 1")
            environ["test.route"] = "GET /users/<id>"  # what a router would set
            seen["after"] = sdk.comment("SELECT 1")
            start_response("200 OK", [])
            return [b"ok"]

        mw = wsgi.TraceletMiddleware(app, route=lambda env: env.get("test.route"))
        body = mw({"HTTP_TRACEPARENT": TP, "PATH_INFO": "/users/42"}, lambda *a: None)
        self.assertEqual(body, [b"ok"])
        self.assertNotIn("route=", seen["before"])
        self.assertIn("traceparent='00-4bf92f3577b34da6a3ce929d0e0e4736-", seen["after"])
        self.assertIn("route='GET%20%2Fusers%2F%3Cid%3E'", seen["after"])
        self.assertNotIn("%2F42", seen["after"])  # the raw path never leaks (ids may appear in the random span id)

    def test_asgi_reads_fastapi_route_after_routing(self):
        seen = {}

        class Route:
            path = "/items/{item_id}"

        async def app(scope, receive, send):
            seen["before"] = sdk.comment("SELECT 1")
            scope["route"] = Route()  # FastAPI sets this when a route matches
            await asyncio.sleep(0)
            seen["after"] = sdk.comment("SELECT 1")

        scope = {"type": "http", "method": "GET", "path": "/items/7", "headers": [(b"traceparent", TP.encode())]}
        asyncio.run(asgi.TraceletMiddleware(app)(scope, None, None))
        self.assertNotIn("route=", seen["before"])
        self.assertIn("route='GET%20%2Fitems%2F%7Bitem_id%7D'", seen["after"])
        self.assertIn("4bf92f3577b34da6a3ce929d0e0e4736", seen["after"])

    def test_asgi_passes_other_scopes_through(self):
        calls = []

        async def app(scope, receive, send):
            calls.append(sdk.current_request())

        asyncio.run(asgi.TraceletMiddleware(app)({"type": "lifespan"}, None, None))
        self.assertEqual(calls, [None])


@unittest.skipUnless(installed("sqlalchemy"), "sqlalchemy not installed")
class SQLAlchemyTest(unittest.TestCase):
    def test_tags_statements_and_queries_still_run(self):
        from sqlalchemy import create_engine, event, text

        from tracelet_sdk.sqlalchemy import instrument

        engine = instrument(create_engine("sqlite://"))
        sent = []
        event.listen(engine, "after_cursor_execute", lambda conn, cur, stmt, *a: sent.append(stmt))
        with engine.connect() as conn:
            with sdk.request(route="GET /r", traceparent=TP):
                value = conn.execute(text("SELECT :x + 1"), {"x": 41}).scalar()
            conn.execute(text("SELECT 1"))
        self.assertEqual(value, 42)
        self.assertRegex(sent[-2], r"^SELECT \? \+ 1 /\*route='GET%20%2Fr',traceparent='00-")
        self.assertEqual(sent[-1], "SELECT 1")


@unittest.skipUnless(installed("django"), "django not installed")
class DjangoTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        import django
        from django.conf import settings

        if not settings.configured:
            settings.configure(
                DATABASES={"default": {"ENGINE": "django.db.backends.sqlite3", "NAME": ":memory:"}},
                ROOT_URLCONF=__name__,
                ALLOWED_HOSTS=["*"],
            )
            django.setup()

    def test_tags_queries_with_url_pattern(self):
        from django.db import connection
        from django.test import RequestFactory
        from django.urls import resolve

        from tracelet_sdk.django import TraceletMiddleware

        sent = []

        def get_response(request):
            request.resolver_match = resolve("/users/42/")

            def capture(execute, sql, params, many, context):
                sent.append(sql)
                return execute(sql, params, many, context)

            with connection.execute_wrapper(capture), connection.cursor() as cur:
                cur.execute("SELECT %s + 1", [41])  # %-formatted by the backend
                value = cur.fetchone()[0]
                cur.execute("SELECT 1")  # no params: sent as-is
            return value

        req = RequestFactory().get("/users/42/", HTTP_TRACEPARENT=TP)
        self.assertEqual(TraceletMiddleware(get_response)(req), 42)
        self.assertRegex(sent[0], r"^SELECT %s \+ 1 /\*route='GET%%20%%2Fusers%%2F%%3Cint%%3Apk%%3E%%2F',traceparent='00-4bf92f")
        self.assertRegex(sent[1], r"^SELECT 1 /\*route='GET%20%2Fusers")
        self.assertNotIn("%%2F42", sent[0])  # the raw path never leaks


@unittest.skipUnless(installed("flask"), "flask not installed")
class FlaskTest(unittest.TestCase):
    def test_tags_with_flask_rule(self):
        from flask import Flask

        from tracelet_sdk.flask import instrument

        app = instrument(Flask(__name__))

        @app.get("/orders/<int:order_id>")
        def order(order_id):
            return sdk.comment("SELECT 1")

        sql = app.test_client().get("/orders/9", headers={"traceparent": TP}).get_data(as_text=True)
        self.assertRegex(sql, r"route='GET%20%2Forders%2F%3Cint%3Aorder_id%3E'")
        self.assertIn("4bf92f3577b34da6a3ce929d0e0e4736", sql)
        self.assertNotRegex(sql, re.escape("/orders/9"))


# Django URLconf for DjangoTest.
def _view(request, pk):
    return None


try:
    from django.urls import path

    urlpatterns = [path("users/<int:pk>/", _view)]
except ImportError:  # pragma: no cover
    urlpatterns = []


if __name__ == "__main__":
    unittest.main()
