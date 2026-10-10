FROM python:3.11-slim@sha256:e41613d42d4891e4930f79523f93f81bbc7632584ec65e36ab055f41a800b41e
WORKDIR /app
COPY requirements.lock ./
RUN pip install --no-cache-dir -r requirements.lock
COPY pyproject.toml README.md ./
COPY src ./src
COPY scripts ./scripts
ENV PYTHONPATH=/app/src PYTHONUNBUFFERED=1
RUN useradd --uid 10001 --create-home monitor && mkdir /data && chown monitor:monitor /data
USER monitor
CMD ["uvicorn", "presales_monitor.lab:app", "--host", "0.0.0.0", "--port", "8000", "--no-access-log"]
