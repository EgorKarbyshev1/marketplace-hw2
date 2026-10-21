# План защиты

1. Запустить Docker Desktop и `docker compose up --build`. Показать логи старта и Flyway.
2. Открыть http://localhost:8080/swagger/. Подготовить USER, SELLER и ADMIN командой из README. Пароли вводить самостоятельно, токены не публиковать.
3. Войти как SELLER через `/auth/login`, вставить access token в Authorize. Создать товар ценой 100 и остатком 10. Показать seller_id.
4. Создать промокод на 20%, минимальную сумму 150. Точные JSON-примеры есть в README.
5. Войти как USER. Показать запрет POST /products (403). Создать заказ на две штуки: сумма 160, скидка 40, остаток 8.
6. Выполнить GET заказа. Войти другим USER — получить 403. Под ADMIN тот же заказ будет доступен.
7. Обновить заказ на одну штуку: промокод снимется, сумма станет 100, остаток 9. Отменить заказ — остаток вернётся к 10. Повторить отмену — 409.
8. Показать неверный токен (401), недостаточный stock (409), неправильную цену (400) и недействующий промокод (422).
9. Показать JSON-логи через `docker compose logs -f api`, сопоставить request_id с X-Request-Id в ответе. Пароль и токены замаскированы.
10. Выполнить SELECT в PostgreSQL, затем показать код и ответить на вопросы. Остановить через `docker compose down`, без -v.

## SELECT для демонстрации

Открыть psql:

```bash
docker compose exec db psql -U marketplace -d marketplace
```

```sql
SELECT id,email,role FROM users;
SELECT id,name,price,stock,status,seller_id FROM products;
SELECT id,user_id,status,total_amount,discount_amount,promo_code_id FROM orders;
SELECT order_id,product_id,quantity,price_at_order FROM order_items;
SELECT code,current_uses,max_uses FROM promo_codes;
SELECT user_id,operation_type,created_at FROM user_operations;
SELECT version,description,type,success FROM flyway_schema_history ORDER BY installed_rank;
```

Не выводить на экран пароли, password_hash, refresh token hashes и JWT-секрет. Выйти из psql: `\q`.

## Что показать в коде

- `api/openapi.yaml`: схемы, ограничения, bearerAuth и ответы ошибок.
- `Makefile` и `Dockerfile`: генерация oapi-codegen до сборки. generated-файл вручную не меняется и не хранится в Git.
- `internal/handler/middleware.go`: проверка токена, роли, request_id и маскирование логов.
- `internal/security/token.go`: HS256, срок действия, issuer и разные типы токенов.
- `internal/service/auth.go`: bcrypt, вход и refresh; `internal/repository/user.go`: хеши сессий и ротация.
- `internal/repository/product.go`: seller_id берётся из проверенного контекста; изменение чужого товара от SELLER блокируется условием SQL.
- `internal/service/order.go`: порядок проверок и одна транзакция на операцию; `internal/repository/order.go`: блокировки и SQL.
- `internal/database/database.go`: проверка старой схемы, baseline 2, затем Flyway migrate. Старый golang-migrate больше не используется.
- `internal/handler/auth_integration_test.go` и `order_integration_test.go`: роли, токены, откаты и конкурентные заказы.

## Короткие вопросы и ответы

1. Что такое OpenAPI? Контракт API: маршруты, запросы, ответы и ограничения.
2. Зачем генерация? Чтобы HTTP-модели и интерфейс обработчика соответствовали контракту. Здесь используется oapi-codegen для Go.
3. Чем PUT отличается от POST? POST создаёт ресурс или выполняет отдельное действие, PUT заменяет поля существующего ресурса.
4. Что такое JWT? Подписанный набор claims. Подпись защищает от изменения, но не скрывает содержимое.
5. Что проверяется в JWT? Подпись HS256, issuer, срок действия, тип токена, user_id и роль. Пользователь и роль дополнительно сверяются с БД.
6. Чем access отличается от refresh? Access действует 20 минут и открывает API, refresh действует 14 дней и используется только для получения новой пары.
7. Почему bcrypt? Это медленный алгоритм хеширования паролей с солью. Пароль нельзя получить обычным обратным преобразованием.
8. Почему регистрация не создаёт ADMIN? Иначе любой мог бы получить все права. Учебные роли создаются отдельной локальной командой с явным разрешением.
9. Чем аутентификация отличается от авторизации? Первая определяет пользователя, вторая проверяет разрешения на действие.
10. Что даёт транзакция? Либо все изменения заказа сохраняются, либо все откатываются.
11. Как защищены остатки? Строки товаров блокируются FOR UPDATE в одинаковом порядке; другие запросы ждут освобождения блокировок.
12. Зачем блокировка пользователя? Чтобы два параллельных запроса не обходили ограничение частоты и проверку активного заказа.
13. Почему деньги не float64? Двоичное представление даёт ошибки округления. Здесь используются копейки и целочисленная арифметика.
14. Зачем Flyway baseline? Чтобы принять уже проверенную схему версии 2 без повторного создания таблиц и затем применить V3. Для новой базы выполняются все миграции.
15. Что даёт request_id? По UUID из ответа можно найти лог конкретного запроса, включая ошибки и длительность.
