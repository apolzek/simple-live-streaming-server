# Versão clássica (nginx-rtmp)

A primeira versão deste projeto, gravada para o [vídeo no YouTube](https://youtu.be/CrEzeBwLZPU):
um único NGINX com o módulo RTMP recebendo do OBS e gerando HLS. Mantida para comparação com o StreamLab.

```bash
cd classic && docker compose up
# OBS → Servidor: rtmp://localhost:1935/live   (chave: qualquer coisa, sem autenticação)
# Assistir (Safari ou extensão HLS no Chrome): http://localhost:8081/live/<chave>.m3u8
```

O que ela **não** tem e o StreamLab adiciona: stream key, múltiplas qualidades (ABR), segmentos CMAF,
cache/CDN, LL-HLS, WebRTC, SRT, chat, interface web e observabilidade.
