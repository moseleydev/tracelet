# tracelet-sdk (Python)

Tags each database query with the request that ran it, so [Tracelet](https://github.com/moseleydev/tracelet) can show per-request waterfalls and catch N+1 loops. Zero dependencies. Python 3.8+.

```bash
pip install tracelet-sdk
```

## Django

```python
MIDDLEWARE = [
    "tracelet_sdk.django.TraceletMiddleware",
    # ...
]
```

That's all. Every query Django runs during a request carries the URL pattern (`GET /users/<int:pk>/`) and a trace id.

## Flask

```python
from tracelet_sdk.flask import instrument
from tracelet_sdk.sqlalchemy import instrument as instrument_engine

instrument(app)
instrument_engine(db.engine)  # Flask-SQLAlchemy, or any SQLAlchemy engine
```

## FastAPI / Starlette / any ASGI app

```python
from tracelet_sdk.asgi import TraceletMiddleware
from tracelet_sdk.sqlalchemy import instrument

app.add_middleware(TraceletMiddleware)  # reads the matched FastAPI route
instrument(engine)
```

Plain WSGI apps can use `tracelet_sdk.wsgi.TraceletMiddleware(app, route=...)`.

## Anything else

```python
import tracelet_sdk

with tracelet_sdk.request(route="POST /import"):
    cursor.execute(tracelet_sdk.comment("SELECT * FROM jobs WHERE id = %s", escape_percent=True), (job_id,))
```

Pass `escape_percent=True` when the driver %-formats the query (psycopg, MySQLdb and Django with parameters). The comment's URL-encoded values contain `%`, which must then be doubled. The Django and SQLAlchemy integrations do this for you.

## Notes

- **Traces:** an incoming `traceparent` header is continued, so traces line up with OpenTelemetry.
- **No raw URLs:** paths are never recorded, so ids and other values in them don't end up in your stats.
- **Existing comments:** SQL that already contains a comment is left unchanged, as the sqlcommenter spec requires.
- **Streaming responses:** in WSGI, queries made while a streamed response is iterated are not tagged.
