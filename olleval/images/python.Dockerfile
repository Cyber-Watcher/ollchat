# Образ проверки задач на Python.
#
# Контейнер проверки работает без сети, поэтому pytest и линтер ставятся
# при сборке образа. Ставить что-либо внутри проверки нельзя: падение
# установки списалось бы на модель, хотя она ни при чём.
FROM python:3.13-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    PYTEST_ADDOPTS=-q

RUN pip install --no-cache-dir pytest==8.3.* ruff==0.7.* && \
    mkdir -p /tmp/pycache && chmod -R a+rwX /tmp/pycache

WORKDIR /w
