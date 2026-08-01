FROM node:22-alpine
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts && npm cache clean --force
COPY server.mjs ./
ENV PORT=8080
EXPOSE 8080
USER node
CMD ["node", "server.mjs"]
