# multi-stage: один бинарь без cgo (modernc.org/sqlite — pure Go)
FROM golang:1.25 AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /diarybot ./cmd/bot

FROM gcr.io/distroless/static-debian12
WORKDIR /app
COPY --from=build /diarybot /app/diarybot
CMD ["/app/diarybot"]
