FROM node:24-alpine
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev && npm cache clean --force
COPY server.js index.html ./
# The image's node user, and a /data it owns: a new volume mounted there
# starts as a copy of it, so the app can write without root.
RUN mkdir /data && chown node /data
USER node
ENV DATA_DIR=/data PORT=3000 NODE_ENV=production
EXPOSE 3000
# node runs as PID 1 and handles SIGTERM itself (server.js).
CMD ["node", "server.js"]
