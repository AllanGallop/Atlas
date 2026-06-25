FROM rust:1.88-bookworm AS build

WORKDIR /src
COPY Cargo.toml ./
RUN mkdir -p src/bin && echo 'fn main() {}' > src/main.rs && echo 'fn main() {}' > src/bin/ct_ingestor.rs && cargo build --release --bin atlas-worker && rm -rf src

COPY src ./src
RUN touch src/main.rs && cargo build --release --bin atlas-worker

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=build /src/target/release/atlas-worker /app/atlas-worker
CMD ["/app/atlas-worker"]
