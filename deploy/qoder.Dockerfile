FROM node:20-alpine
RUN apk add --no-cache git \
    && npm install -g @qodercn-ai/qoderclicn \
    && git clone --depth 1 https://github.com/avaritiachaos/qoder-proxy.git /app \
    && cd /app \
    && npm install --production \
    && apk del git
WORKDIR /app
ENV PORT=3000
ENV CLI_BACKEND=cn
ENV PROXY_API_KEY=""
EXPOSE 3000
CMD ["node", "clean/server.js"]
