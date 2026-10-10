import os
import secrets

from fastapi import Depends, HTTPException
from fastapi.security import HTTPAuthorizationCredentials, HTTPBearer

bearer = HTTPBearer(auto_error=False)


def authenticate(credentials: HTTPAuthorizationCredentials | None = Depends(bearer)):
    token = os.environ.get("MON_ADMIN_TOKEN", "")
    if len(token) < 24 or credentials is None or not secrets.compare_digest(credentials.credentials, token):
        raise HTTPException(401, "Authentication required")
    return "lab-operator"
