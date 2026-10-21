# Marketplace

В проекте я сделал API товаров, заказов и промокодов на Go. Используются net/http, pgx, PostgreSQL, oapi-codegen, Flyway, bcrypt и JWT. Логика разделена на handler, service и repository. HTTP-модели и интерфейс обработчиков генерируются из `api/openapi.yaml`, вручную DTO не пишутся.

## Запуск

Нужны Docker Desktop и Docker Compose с запущенным движком. Один раз создайте локальный JWT-секрет в PowerShell:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/setup-local.ps1
```

Скрипт создаст случайный секрет в `.env`. Существующий файл он не перезаписывает. `.env` исключён из Git, публиковать его не нужно. Затем запуск одной командой:

```bash
docker compose up --build
```

Swagger: http://localhost:8080/swagger/. Его интерфейс загружается с CDN, браузеру нужен интернет. API работает на порту 8080, база — на 5432.

Миграции применяются Flyway при запуске API. Старую проверенную схему golang-migrate версии 2 приложение ставит на Flyway baseline 2 и применяет V3. Перед baseline проверяются версия, отсутствие dirty-состояния, колонки и индексы. Неизвестная схема не принимается автоматически. Таблица `schema_migrations` остаётся исторической, golang-migrate больше не запускается. На чистой базе Flyway применяет V1, V2 и V3.

V3 добавляет пользователей, refresh-сессии и `seller_id`. Старые товары остаются с `seller_id = NULL`, управлять ими может ADMIN. Старые заказы и промокоды сохраняются. Не используйте `down -v`, `clean` или очистку таблиц.

## Аккаунты и JWT

Публичная регистрация `POST /auth/register` создаёт только USER:

```json
{"email":"student@example.test","password":"ChooseYourOwnPassword123"}
```

Для входа отправьте эти же поля в `POST /auth/login`. Ответ содержит `access_token`, `refresh_token`, `token_type` и `expires_in`. Нажмите **Authorize** в Swagger и вставьте только access token, без слова Bearer. Для перехода к другому пользователю сначала нажмите Logout в этом окне.

`POST /auth/refresh` принимает:

```json
{"refresh_token":"вставьте refresh token"}
```

Refresh одноразовый: после обновления используйте новую пару токенов. В базе хранится только SHA-256 хеш refresh-токена. Access действует по умолчанию 20 минут, refresh — 14 дней. Переменные `ACCESS_TOKEN_MINUTES` допускают 15–30, `REFRESH_TOKEN_DAYS` — 7–30. После изменения настроек пересоздайте API через `docker compose up -d api`.

Пароли хешируются bcrypt. Пароль должен содержать 8–72 UTF-8 байта, email должен быть уникальным. Роль хранится в users и access JWT; при проверке сверяется с базой. Пароль, роль ADMIN и user_id нельзя подставить в запрос для получения чужих прав. Идентификатор берётся из проверенного JWT, временного заголовка пользователя больше нет.

Для локальной демонстрации трёх ролей один раз выполните:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/create-demo.ps1
```

Скрипт спросит пароль без отображения ввода и создаст `user@local.test`, `seller@local.test`, `admin@local.test`. Это отдельная команда, а не публичный endpoint. Она не перезаписывает существующие аккаунты. Затем войдите каждым через `/auth/login`. Для команды требуется явно разрешённый режим `ALLOW_DEMO_USERS=true`, который скрипт передаёт только запускаемому процессу.

## Товары, заказы и роли

Все операции API защищены, кроме трёх методов auth и документации. USER читает товары и управляет своими заказами. SELLER читает товары, создаёт промокоды и управляет только своими товарами. ADMIN управляет любыми товарами и заказами и создаёт промокоды. Заказы SELLER создавать и читать не может.

От SELLER выполните `POST /products`:

```json
{
  "name":"Keyboard",
  "description":"USB keyboard",
  "price":100.00,
  "stock":10,
  "category":"electronics",
  "status":"ACTIVE"
}
```

Ожидается 201, `seller_id` совпадает с ID SELLER. USER получит 403. Скопируйте ID товара. `GET /products/{id}` показывает товар, а `GET /products?page=0&size=5&status=ACTIVE&category=electronics` — список с `items`, `totalElements`, `page`, `size`. По умолчанию size=20, допустимо 1–100. Категория сравнивается точно, архивные товары доступны для чтения.

`PUT /products/{id}` принимает все обязательные поля создания. Описание можно пропустить: оно станет null. `DELETE /products/{id}` возвращает 204 и переводит товар в ARCHIVED, строка остаётся в БД. Чужой SELLER получит ACCESS_DENIED, ADMIN сможет изменить любой товар.

От SELLER или ADMIN создайте `POST /promo-codes`:

```json
{
  "code":"STUDENT20",
  "discount_type":"PERCENTAGE",
  "discount_value":20,
  "min_order_amount":150,
  "max_uses":5,
  "valid_from":"2026-01-01T00:00:00Z",
  "valid_until":"2027-12-31T23:59:59Z",
  "active":true
}
```

Код должен быть уникальным. USER получит 403. Для фиксированной скидки используется FIXED_AMOUNT.

Войдите как USER и отправьте `POST /orders`, заменив значение product_id:

```json
{
  "items":[{"product_id":"UUID товара","quantity":2}],
  "promo_code":"STUDENT20"
}
```

Ожидается 201: CREATED, total_amount=160.00, discount_amount=40.00. Остаток станет 8, использование промокода — 1. `promo_code` необязателен. Заказ содержит снимок `price_at_order`, который не меняется вслед за ценой товара.

Посмотрите `GET /orders/{id}`. Другой USER получит ORDER_OWNERSHIP_VIOLATION (403), ADMIN сможет прочитать заказ. Это также относится к изменению и отмене.

`PUT /orders/{id}` заменяет позиции только в CREATED:

```json
{"items":[{"product_id":"UUID товара","quantity":1}]}
```

В примере сумма станет 100.00, промокод снимется из-за минимальной суммы, его счётчик вернётся к 0, остаток — к 9. При обновлении новые позиции получают текущие цены. `POST /orders/{id}/cancel` вызывается без тела, возвращает остатки и переводит заказ в CANCELED. Повторная отмена даёт 409 и не возвращает остатки ещё раз.

Для демонстрации состояний есть `POST /orders/{id}/status` с телом `{"status":"PAYMENT_PENDING"}`. Разрешена только цепочка CREATED → PAYMENT_PENDING → PAID → SHIPPED → COMPLETED. Отмена — отдельным методом из CREATED или PAYMENT_PENDING. Реальная оплата и доставка не реализованы.

Заказ допускает 1–50 уникальных товаров, quantity=1–999. Деньги считаются в копейках без float64. Процентная скидка округляется до копейки и не превышает 70%. Если задан процент больше 70, скидка не применяется, цена остаётся полной. Так трактуется неоднозначная оговорка задания.

Все изменения заказа выполняются одной транзакцией. Товары блокируются FOR UPDATE в порядке UUID, промокод — перед изменением счётчика. Advisory-блокировка пользователя и уникальный индекс активного заказа защищают параллельные запросы. Ошибка откатывает остатки, позиции, суммы и счётчики.

`ORDER_OPERATION_INTERVAL_MINUTES` задаёт интервал между созданием и между обновлением заказа. По умолчанию 1 минута, 0 отключает ограничение для демонстрации. Отмена не сбрасывает время создания.

## Ошибки и логи

Ошибки имеют JSON-формат `error_code`, `message`, необязательное `details`. Неверные поля, UUID, параметры и JSON дают 400 VALIDATION_ERROR с причинами по полям. Принимается ровно один JSON-объект. Неизвестный маршрут — 404, неверный метод — 405 с Allow.

На защите можно показать TOKEN_INVALID без токена (401), TOKEN_EXPIRED с просроченным access (401), ACCESS_DENIED для USER при создании товара (403), чужой заказ (403), PRODUCT_NOT_FOUND (404), недостаток stock (409), повторную отмену (409), недействующий промокод (422) и частые операции (429). Refresh не принимается вместо access.

Каждый HTTP-запрос записывается одной JSON-записью с request_id, методом, путём, статусом, длительностью, user_id и временем. Для POST, PUT и DELETE сохраняется замаскированное тело. Пароли и токены в логи не записываются, заголовок Authorization тоже не логируется.

```bash
docker compose logs -f api
curl.exe -i http://localhost:8080/products
```

Во второй команде без токена будет 401 и заголовок X-Request-Id. Этот UUID можно найти в JSON-логе. Для успешного запроса user_id содержит ID проверенного пользователя, иначе null.

## База и тесты

Подключение: localhost:5432, база marketplace, пользователь и пароль marketplace. Это локальные учебные настройки.

```bash
docker compose exec db psql -U marketplace -d marketplace -c "SELECT id,email,role,created_at FROM users;"
docker compose exec db psql -U marketplace -d marketplace -c "SELECT id,name,price,stock,status,seller_id FROM products;"
docker compose exec db psql -U marketplace -d marketplace -c "SELECT id,user_id,status,total_amount,discount_amount,promo_code_id FROM orders;"
docker compose exec db psql -U marketplace -d marketplace -c "SELECT order_id,product_id,quantity,price_at_order FROM order_items;"
docker compose exec db psql -U marketplace -d marketplace -c "SELECT code,current_uses,max_uses FROM promo_codes;"
docker compose exec db psql -U marketplace -d marketplace -c "SELECT user_id,operation_type,created_at FROM user_operations;"
docker compose exec db psql -U marketplace -d marketplace -c "SELECT user_id,expires_at FROM refresh_tokens;"
docker compose exec db psql -U marketplace -d marketplace -c "SELECT version,description,type,success FROM flyway_schema_history ORDER BY installed_rank;"
```

Для локальной сборки нужны Go 1.25+ и сгенерированный код:

```bash
go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.4.1 --config api/oapi-codegen.yaml api/openapi.yaml
go build ./...
go test ./...
```

Можно использовать `make generate`, `make build`, `make test`. Без TEST_DATABASE_URL интеграционные тесты пропускаются. Для них создайте отдельную базу с суффиксом _test (один раз) и примените Flyway:

```bash
docker compose exec db psql -U marketplace -d postgres -c "CREATE DATABASE marketplace_security_test;"
docker compose run --rm --no-deps -e DATABASE_URL=postgres://marketplace:marketplace@db:5432/marketplace_security_test?sslmode=disable api migrate
```

В PowerShell:

```powershell
$env:TEST_DATABASE_URL = 'postgres://marketplace:marketplace@localhost:5432/marketplace_security_test?sslmode=disable'
$env:MIGRATIONS_MODE = 'verify'
go test -count=1 -v ./...
```

verify только проверяет уже применённый Flyway V3 и разрешён лишь для тестовых баз. Тесты создают свои аккаунты и данные в отдельной БД. Рабочая база для тестов не очищается. Для запуска Go без Docker дополнительно нужны Flyway CLI и переменная FLYWAY_BINARY с путём к нему; проще запускать API через Compose.

Остановка без удаления данных:

```bash
docker compose down
```
