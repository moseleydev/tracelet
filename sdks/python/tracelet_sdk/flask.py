"""Flask integration: ``tracelet_sdk.flask.instrument(app)``."""

from typing import Any, Dict, Optional

from .wsgi import TraceletMiddleware


def _route(environ: Dict[str, Any]) -> Optional[str]:
    from flask import has_request_context, request

    if has_request_context() and request.url_rule is not None:
        return "{} {}".format(request.method, request.url_rule.rule)
    return None


def instrument(app: Any) -> Any:
    """Tag queries with the Flask route that ran them ("GET /users/<int:id>").
    Combine with ``tracelet_sdk.sqlalchemy.instrument`` (Flask-SQLAlchemy:
    ``instrument(db.engine)``) or call ``tracelet_sdk.comment`` yourself."""
    app.wsgi_app = TraceletMiddleware(app.wsgi_app, route=_route)
    return app
