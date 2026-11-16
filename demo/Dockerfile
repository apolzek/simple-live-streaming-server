# ffmpeg + uma fonte (o drawtext precisa de uma para desenhar o relógio)
FROM alpine:3.23
RUN apk add --no-cache ffmpeg font-dejavu
COPY publish.sh /publish.sh
ENTRYPOINT ["/bin/sh", "/publish.sh"]
