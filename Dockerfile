FROM alpine:3.12.0
COPY ./app /app
CMD [/app help]