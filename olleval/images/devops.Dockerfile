# Образ проверки задач DevOps: линтеры конфигов и разборщики манифестов.
# Собирается из готовых образов инструментов, чтобы не тянуть их по одному
# и не зависеть от сети в момент проверки.
FROM hadolint/hadolint:latest-debian AS hadolint
FROM hashicorp/terraform:latest AS terraform

FROM debian:13-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
        shellcheck yamllint nginx-light systemd python3-yaml jq ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=hadolint /bin/hadolint /usr/local/bin/hadolint
COPY --from=terraform /bin/terraform /usr/local/bin/terraform

WORKDIR /w
