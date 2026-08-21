# Образ проверки задач на TypeScript, Angular и Vue.
#
# Все зависимости ставятся при сборке образа и лежат в /opt/skel/node_modules:
# контейнер проверки работает без сети, и `npm install` внутри него уронил бы
# проверку — а списалось бы это на модель, хотя она ни при чём.
FROM node:22

ENV NODE_PATH=/opt/skel/node_modules \
    NPM_CONFIG_UPDATE_NOTIFIER=false \
    NPM_CONFIG_FUND=false

# Ставится в два захода и с --legacy-peer-deps: npm разрешает зависимости
# по-разному в зависимости от порядка и состояния кеша, и одиночная команда
# падала с ERESOLVE — «vite@undefined» при живом vite@5 в том же списке.
# Проверка peer-зависимостей нам здесь не нужна: образ одноразовый и без сети.
RUN mkdir -p /opt/skel && cd /opt/skel && npm init -y >/dev/null && \
    npm install --no-audit --no-fund --legacy-peer-deps \
      typescript@5 vite@5 vitest@2 >/dev/null && \
    npm install --no-audit --no-fund --legacy-peer-deps \
      @vitest/coverage-v8@2 vue@3 @vue/test-utils@2 @vitejs/plugin-vue@5 \
      @angular/core@18 @angular/common@18 @angular/compiler@18 \
      rxjs@7 zone.js@0.14 tslib@2 @types/node@22 >/dev/null && \
    npm install -g typescript@5 vitest@2 >/dev/null && \
    chmod -R a+rwX /opt/skel

WORKDIR /w
