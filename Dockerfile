# ---- build stage -------------------------------------------------------------
FROM golang:1.26-alpine AS build

WORKDIR /src

# Dependencies are cached in their own layer so code edits do not re-download them.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/bot ./cmd/bot
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/seed ./cmd/seed
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/import ./cmd/import
# Where uploaded price lists wait for "Apply". A volume mounted there starts
# with this directory's owner, so the bot, not root, can write to it.
RUN mkdir -p /out/imports

# ---- runtime stage -----------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/bot /bot
COPY --from=build /out/seed /seed
COPY --from=build /out/import /import
# The vehicle directory ships with the image, so a server loads it with
# `--entrypoint /import bot -vehicles /data/vehicles.csv`, no copying.
COPY data /data
COPY --from=build --chown=nonroot:nonroot /out/imports /imports

USER nonroot:nonroot
ENTRYPOINT ["/bot"]
