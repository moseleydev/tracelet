"""SQLAlchemy integration: ``tracelet_sdk.sqlalchemy.instrument(engine)``."""

from typing import Any

from . import comment


def instrument(engine: Any) -> Any:
    """Tag every statement the engine runs inside a request context.
    Works with any driver; % signs are escaped for drivers that %-format."""
    from sqlalchemy import event

    target = getattr(engine, "sync_engine", engine)  # AsyncEngine wraps a sync engine

    @event.listens_for(target, "before_cursor_execute", retval=True)
    def _tag(conn, cursor, statement, parameters, context, executemany):
        formats = conn.dialect.paramstyle in ("format", "pyformat") and parameters is not None
        return comment(statement, escape_percent=formats), parameters

    return engine
