# ---- build stage -------------------------------------------------------------
FROM golang:1.26-alpine AS build

WORKDIR /src

# Dependencies are cached in their own layer so code edits do not re-download them.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/bot ./cmd/bot
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/seed ./cmd/seed

# ---- runtime stage -----------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/bot /bot
COPY --from=build /out/seed /seed

USER nonroot:nonroot
ENTRYPOINT ["/bot"]
