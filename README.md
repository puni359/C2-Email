
# Email C2 (Command & Control)

## Гайд по поднятию SMTP 
https://concrete-swift-b54.notion.site/Email-C2-14686eb850e9806f80b4cd721a44b67f?pvs=4

## Описание
Email C2 - это инструмент командного управления, использующий электронную почту в качестве скрытого канала связи. Проект разработан на языке Go 
## Особенности
- Скрытая коммуникация через SMTP/IMAP протоколы
- JSON-форматированные сообщения
- Уникальные UUID для каждой сессии
- Это не обычно
- Кросс-платформенная совместимость (Windows/Linux/macOS)

## Требования
- Go 1.16 или выше
- Доступ к SMTP и IMAP серверам ( или поднять свой)
- Учетные записи электронной почты для сервера и клиента

## Зависимости
```go
require (
    github.com/emersion/go-imap
    github.com/emersion/go-sasl
    github.com/google/uuid
    gopkg.in/gomail.v2
)
```

## Установка
1. Клонируйте репозиторий:
```bash
git clone https://github.com/puni359/C2-Email
```

2. Перейдите в директорию проекта:
```bash
cd С2-Email
```

3. Установите зависимости:
```bash
go mod download
```

## Использование

### Сервер
```bash
go run cmd/server/main.go -imap "mail.server.com:993" -smtp "mail.server.com" -email "server@example.com" -client "client@example.com" -password "server_password"
```

Параметры сервера:
- `-imap`: Адрес IMAP сервера с портом
- `-smtp`: Адрес SMTP сервера
- `-email`: Email адрес сервера
- `-client`: Email адрес клиента
- `-password`: Пароль от почтового ящика сервера

### Клиент
```bash
go run cmd/client/main.go -imap "mail.server.com:993" -smtp "mail.server.com" -email "client@example.com" -recipient "server@example.com" -password "client_password"
```

Параметры клиента:
- `-imap`: Адрес IMAP сервера с портом
- `-smtp`: Адрес SMTP сервера
- `-email`: Email адрес клиента
- `-recipient`: Email адрес сервера
- `-password`: Пароль от почтового ящика клиента

## Принцип работы
1. Клиент подключается и генерирует уникальный UUID сессии
2. Сервер отправляет команды в формате JSON (могут быть баги из за RFC акуратнее)
3. Клиент выполняет команды и отправляет результаты
4. Сервер получает и обрабатывает ответы

## Структура сообщений
```json
{
    "type": "command/response",
    "uuid": "уникальный-идентификатор-сессии",
    "content": "содержимое-команды-или-ответа",
    "timestamp": 1234567890
}
```

## Безопасность
⚠️ Важные замечания:
- Отсутствует дополнительное шифрование сообщений
- Проект предназначен для исследовательских целей
- Не рекомендуется использовать в проде (хотя вам решать 

## Планируемые улучшения
- Добавление end-to-end шифрования
- Улучшенная система логирования
- Поддержка конфигурационных файлов
- Расширенная обработка ошибок
- Улучшенный механизм аутентификации
- WinApi outlook а почему нет? 

## Отказ от ответственности
Данный инструмент создан исключительно в исследовательских и образовательных целях. Автор не несет ответственности за любое неправомерное использование данного программного обеспечения.



