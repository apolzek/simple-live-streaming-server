#!/bin/sh
# Simula um streamer usando o OBS:
#  - vídeo 1280x720 @ 30fps com barras de teste e o RELÓGIO (UTC) gravado
#    na imagem (compare com o relógio da página para ver a latência "glass-to-glass")
#  - áudio senoidal 440Hz
#  - H.264 com keyframe a cada 2s (o que a Twitch recomenda) e CBR 3 Mbps
#
# Equivalente no OBS: Saída > Transmissão > Codificador x264, Taxa de bits 3000,
# Intervalo de quadro-chave 2s, Predefinição veryfast, Perfil main.

set -u
URL="${TARGET}/${CHANNEL}?key=${STREAM_KEY}"
echo "publicando em ${TARGET}/${CHANNEL} (stream key oculta)"

while true; do
  ffmpeg -hide_banner -loglevel warning -re \
    -f lavfi -i "testsrc2=size=1280x720:rate=30" \
    -f lavfi -i "sine=frequency=440:sample_rate=48000" \
    -vf "drawbox=x=0:y=0:w=iw:h=110:color=black@0.6:t=fill,\
drawtext=fontsize=72:fontcolor=white:x=40:y=20:text='%{gmtime\:%H\\\\\:%M\\\\\:%S} UTC',\
drawtext=fontsize=32:fontcolor=white:x=w-tw-40:y=40:text='StreamLab demo  frame %{n}'" \
    -c:v libx264 -preset veryfast -tune zerolatency -profile:v main -pix_fmt yuv420p \
    -b:v 3000k -maxrate 3000k -bufsize 6000k -g 60 -keyint_min 60 -sc_threshold 0 \
    -c:a aac -b:a 128k -ar 48000 -ac 2 \
    -f flv "$URL"
  echo "ffmpeg saiu ($?), reconectando em 3s..."
  sleep 3
done
