# Образ проверки задач на Rust. Реестр crates внутри не нужен: задачи пишутся
# на стандартной библиотеке — иначе проверка без сети упиралась бы в скачивание
# зависимостей, и это списывалось бы на модель.
FROM rust:1.92

ENV CARGO_HOME=/tmp/cargo \
    CARGO_TERM_COLOR=never

RUN rustup component add clippy && \
    mkdir -p /opt/warm && cd /opt/warm && cargo init --name warm -q && \
    cargo build -q && cargo clippy -q && chmod -R a+rwX /tmp/cargo

WORKDIR /w
