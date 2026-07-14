# leetcode-cli

CLI tool for fetching, testing, and submitting LeetCode solutions from the terminal.

---

## Features

- Fetch problem descriptions in Markdown
- Run Go solutions locally against test cases
- Submit solutions to LeetCode and get results
- Generate worklist of unsolved problems
- Supports leetcode.com and leetcode.cn

## Quick Start

### Prerequisites

- Go 1.23+
- LeetCode account

### Setup

1. Clone the repository
2. Copy `.env.example` to `.env`
3. Fill in your LeetCode session cookies:

```
LEETCODE_SESSION=your_session_cookie
LEETCODE_CSRFTOKEN=your_csrf_token
LEETCODE_CFCLEARANCE=your_cf_clearance
```

To obtain the cookies, open LeetCode in your browser, open DevTools (F12),
go to Application -> Storage -> Cookies, and copy the values.

### Build

```
go build -o leetcode-cli .
```

### Usage

```
leetcode-cli worklist                  # list unsolved problems
leetcode-cli fetch two-sum             # get problem details
leetcode-cli test two-sum -f sol.go    # run locally
leetcode-cli submit two-sum -f sol.go  # submit to LeetCode
```

### Workflow

1. Run `leetcode-cli worklist` to generate a list of unsolved problems
2. Pick a problem and write your solution in a `.go` file
3. Test locally: `leetcode-cli test <slug> -f solution.go`
4. Submit: `leetcode-cli submit <slug> -f solution.go`

All commands output JSON to stdout for easy parsing.

## License

MIT

---

# leetcode-cli

Консольный инструмент для получения, тестирования и отправки решений LeetCode из терминала.

## Возможности

- Загрузка описаний задач в Markdown
- Локальный запуск Go-решений на тестовых данных
- Отправка решений на LeetCode и получение результатов
- Генерация списка нерешённых задач
- Поддержка leetcode.com и leetcode.cn

## Быстрый старт

### Требования

- Go 1.23+
- Аккаунт LeetCode

### Настройка

1. Склонируйте репозиторий
2. Скопируйте `.env.example` в `.env`
3. Заполните cookie-сессии LeetCode:

```
LEETCODE_SESSION=ваша_session_cookie
LEETCODE_CSRFTOKEN=ваш_csrf_token
LEETCODE_CFCLEARANCE=ваш_cf_clearance
```

Чтобы получить cookie, откройте LeetCode в браузере, нажмите F12,
перейдите в Application -> Storage -> Cookies и скопируйте значения.

### Сборка

```
go build -o leetcode-cli .
```

### Использование

```
leetcode-cli worklist                  # список нерешённых задач
leetcode-cli fetch two-sum             # получить описание задачи
leetcode-cli test two-sum -f sol.go    # запустить локально
leetcode-cli submit two-sum -f sol.go  # отправить на LeetCode
```

### Рабочий процесс

1. Запустите `leetcode-cli worklist` для получения списка нерешённых задач
2. Выберите задачу и напишите решение в `.go` файле
3. Протестируйте локально: `leetcode-cli test <slug> -f solution.go`
4. Отправьте: `leetcode-cli submit <slug> -f solution.go`

Все команды выводят JSON в stdout для удобной обработки.

## Лицензия

MIT
