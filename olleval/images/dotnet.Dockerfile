# Образ проверки задач на C#. Восстановление пакетов делается при сборке образа:
# в контейнере проверки сети нет, а `dotnet restore` без неё падает.
FROM mcr.microsoft.com/dotnet/sdk:10.0

ENV DOTNET_CLI_TELEMETRY_OPTOUT=1 \
    DOTNET_NOLOGO=1 \
    DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1 \
    NUGET_PACKAGES=/opt/nuget

# Прогрев: консольный проект и проект тестов подтягивают всё, что нужно xunit,
# и складывают в /opt/nuget — оттуда проверка возьмёт пакеты уже без сети.
RUN mkdir -p /opt/warm && cd /opt/warm && \
    dotnet new console -o app >/dev/null && dotnet build app >/dev/null && \
    dotnet new xunit -o tests >/dev/null && dotnet build tests >/dev/null && \
    chmod -R a+rwX /opt/nuget

WORKDIR /w
