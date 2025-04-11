package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"

	_ "github.com/mattn/go-sqlite3"
	"gopkg.in/telebot.v3"
)

type Employee struct {
	ID            int
	Name          string
	Birthday      time.Time
	ChatID        int64
	LastNotifyDay int
}

var (
	db             *sql.DB
	lastShownLists = make(map[int64][]Employee)
)

func main() {
	// Загружаем .env
	if err := godotenv.Load(); err != nil {
		log.Println("Файл .env не найден")
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)

	if err := initDB(); err != nil {
		log.Fatal("Ошибка инициализации БД:", err)
	}
	defer db.Close()

	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		log.Fatal("Токен бота не указан")
	}

	pref := telebot.Settings{
		Token:  botToken,
		Poller: &telebot.LongPoller{Timeout: 10 * time.Second},
	}

	bot, err := telebot.NewBot(pref)
	if err != nil {
		log.Fatal(err)
	}

	bot.Handle("/start", startHandler)
	bot.Handle("/add", addHandler)
	bot.Handle("/remove", removeHandler)
	bot.Handle("/list", listHandler)
	bot.Handle("/notify", notifyHandler)
	bot.Handle(telebot.OnText, textHandler)

	go startDailyBirthdayChecker(bot)

	log.Println("Бот запущен...")
	bot.Start()
}

func initDB() error {
	var err error
	db, err = sql.Open("sqlite3", "birthdays.db?_foreign_keys=on")
	if err != nil {
		return err
	}

	if err = db.Ping(); err != nil {
		return err
	}

	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS employees (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		birthday DATE NOT NULL,
		chat_id INTEGER NOT NULL,
		last_notify_day INTEGER DEFAULT -1
	);
	CREATE TABLE IF NOT EXISTS active_chats (
		chat_id INTEGER PRIMARY KEY,
		last_active DATE NOT NULL
	)`)
	return err
}

func startHandler(c telebot.Context) error {
	updateActiveChat(c.Chat().ID)
	return c.Send(`📅 Бот для учета дней рождения
Доступные команды:
/add - добавить сотрудника
/remove - удалить сотрудника
/list - список всех сотрудников
/notify - отправить уведомления вручную`)
}

func addHandler(c telebot.Context) error {
	updateActiveChat(c.Chat().ID)
	return c.Send("Введите данные сотрудника в формате:\nИмя Фамилия ДД.ММ.ГГГГ\n\nПример: Иван Иванов 15.05.1990")
}

func removeHandler(c telebot.Context) error {
	chatID := c.Chat().ID
	updateActiveChat(chatID)

	employees, err := getAllEmployees()
	if err != nil {
		log.Println("Ошибка получения списка:", err)
		return c.Send("❌ Ошибка при получении списка сотрудников")
	}

	if len(employees) == 0 {
		return c.Send("ℹ️ Список сотрудников пуст")
	}

	var message strings.Builder
	message.WriteString("Выберите сотрудника для удаления:\n")
	for i, emp := range employees {
		message.WriteString(fmt.Sprintf("%d. %s (%s)\n", i+1, emp.Name, emp.Birthday.Format("02.01.2006")))
	}
	message.WriteString("\nОтправьте номер сотрудника для удаления")

	lastShownLists[chatID] = employees
	return c.Send(message.String())
}

func listHandler(c telebot.Context) error {
	updateActiveChat(c.Chat().ID)
	employees, err := getAllEmployees()
	if err != nil {
		log.Println("Ошибка получения списка:", err)
		return c.Send("❌ Ошибка при получении списка сотрудников")
	}

	if len(employees) == 0 {
		return c.Send("ℹ️ Список сотрудников пуст")
	}

	var message strings.Builder
	message.WriteString("📋 Общий список сотрудников:\n\n")
	for _, emp := range employees {
		message.WriteString(fmt.Sprintf("• %s - %s\n", emp.Name, emp.Birthday.Format("02.01.2006")))
	}

	return c.Send(message.String())
}

func notifyHandler(c telebot.Context) error {
	updateActiveChat(c.Chat().ID)

	employees, err := getAllEmployees()
	if err != nil {
		log.Println("Ошибка получения списка:", err)
		return c.Send("❌ Ошибка при получении списка сотрудников")
	}

	activeChats, err := getAllActiveChats()
	if err != nil {
		log.Println("Ошибка получения активных чатов:", err)
		return c.Send("❌ Ошибка при получении списка чатов")
	}

	hasNotifications := false

	for _, emp := range employees {
		daysUntil := daysUntilBirthday(emp.Birthday)

		if daysUntil == 14 || daysUntil == 7 || daysUntil == 1 || daysUntil == 0 {
			msg := createNotificationMessage(emp.Name, daysUntil, emp.Birthday)

			for _, chatID := range activeChats {
				if _, err := c.Bot().Send(telebot.ChatID(chatID), msg); err != nil {
					log.Printf("Ошибка отправки в чат %d: %v", chatID, err)
				}
			}
			hasNotifications = true
		}
	}

	if !hasNotifications {
		return c.Send("ℹ️ В ближайшие 14 дней дней рождения нет")
	}

	return c.Send("✅ Уведомления отправлены во все активные чаты")
}

func textHandler(c telebot.Context) error {
	text := c.Text()
	chatID := c.Chat().ID
	updateActiveChat(chatID)

	if employees, ok := lastShownLists[chatID]; ok {
		num, err := strconv.Atoi(text)
		if err != nil || num < 1 || num > len(employees) {
			delete(lastShownLists, chatID)
			return c.Send("❌ Неверный номер сотрудника")
		}

		employee := employees[num-1]
		if err := deleteEmployee(employee.ID); err != nil {
			log.Println("Ошибка удаления:", err)
			delete(lastShownLists, chatID)
			return c.Send("❌ Ошибка при удалении сотрудника")
		}

		delete(lastShownLists, chatID)
		return c.Send(fmt.Sprintf("✅ Сотрудник %s удален", employee.Name))
	}

	if !strings.HasPrefix(text, "/") {
		parts := strings.Split(text, " ")
		if len(parts) < 3 {
			return c.Send("❌ Неверный формат. Используйте: Имя Фамилия ДД.ММ.ГГГГ")
		}

		name := strings.Join(parts[:len(parts)-1], " ")
		dateStr := parts[len(parts)-1]

		birthday, err := time.Parse("02.01.2006", dateStr)
		if err != nil {
			return c.Send("❌ Неверный формат даты. Используйте ДД.ММ.ГГГГ")
		}

		if err := addEmployee(name, birthday, chatID); err != nil {
			log.Println("Ошибка добавления:", err)
			return c.Send("❌ Ошибка при добавлении сотрудника")
		}

		return c.Send(fmt.Sprintf("✅ Сотрудник %s добавлен (день рождения: %s)",
			name, birthday.Format("02.01.2006")))
	}

	return nil
}

func addEmployee(name string, birthday time.Time, chatID int64) error {
	_, err := db.Exec(
		"INSERT INTO employees (name, birthday, chat_id) VALUES (?, ?, ?)",
		name, birthday.Format("2006-01-02"), chatID,
	)
	return err
}

func deleteEmployee(id int) error {
	_, err := db.Exec("DELETE FROM employees WHERE id = ?", id)
	return err
}

func getAllEmployees() ([]Employee, error) {
	rows, err := db.Query("SELECT id, name, date(birthday) as birthday, chat_id, last_notify_day FROM employees ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var employees []Employee
	for rows.Next() {
		var emp Employee
		var dateStr string
		if err := rows.Scan(&emp.ID, &emp.Name, &dateStr, &emp.ChatID, &emp.LastNotifyDay); err != nil {
			return nil, err
		}

		emp.Birthday, err = time.Parse("2006-01-02", strings.Split(dateStr, "T")[0])
		if err != nil {
			return nil, fmt.Errorf("ошибка парсинга даты '%s': %v", dateStr, err)
		}

		employees = append(employees, emp)
	}
	return employees, nil
}

func updateActiveChat(chatID int64) {
	_, err := db.Exec("INSERT OR REPLACE INTO active_chats (chat_id, last_active) VALUES (?, CURRENT_DATE)", chatID)
	if err != nil {
		log.Printf("Ошибка обновления активного чата %d: %v", chatID, err)
	}
}

func getAllActiveChats() ([]int64, error) {
	rows, err := db.Query("SELECT chat_id FROM active_chats WHERE last_active > DATE('now', '-30 days')")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chats []int64
	for rows.Next() {
		var chatID int64
		if err := rows.Scan(&chatID); err != nil {
			return nil, err
		}
		chats = append(chats, chatID)
	}
	return chats, nil
}

func startDailyBirthdayChecker(bot *telebot.Bot) {
	location, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		log.Println("Ошибка загрузки локации:", err)
		location = time.UTC
	}

	// Первая проверка сразу при запуске
	checkAndNotifyBirthdays(bot, location)

	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			checkAndNotifyBirthdays(bot, location)
		}
	}
}

func checkAndNotifyBirthdays(bot *telebot.Bot, location *time.Location) {
	now := time.Now().In(location)
	log.Printf("Проверка дней рождения в %s", now.Format("2006-01-02 15:04:05 MST"))

	employees, err := getAllEmployees()
	if err != nil {
		log.Println("Ошибка проверки дней рождения:", err)
		return
	}

	activeChats, err := getAllActiveChats()
	if err != nil {
		log.Println("Ошибка получения списка чатов:", err)
		return
	}

	for _, emp := range employees {
		daysUntil := daysUntilBirthday(emp.Birthday)
		log.Printf("Проверка %s: дней до ДР - %d (последнее уведомление было за %d дней)",
			emp.Name, daysUntil, emp.LastNotifyDay)

		if (daysUntil == 14 || daysUntil == 7 || daysUntil == 1 || daysUntil == 0) &&
			emp.LastNotifyDay != daysUntil {
			msg := createNotificationMessage(emp.Name, daysUntil, emp.Birthday)
			log.Printf("Отправка уведомления: %s", msg)

			for _, chatID := range activeChats {
				if _, err := bot.Send(telebot.ChatID(chatID), msg); err != nil {
					log.Printf("Ошибка отправки в чат %d: %v", chatID, err)
				} else {
					log.Printf("Уведомление отправлено в чат %d", chatID)
				}
			}

			if err := updateLastNotifyDay(emp.ID, daysUntil); err != nil {
				log.Println("Ошибка обновления дня уведомления:", err)
			}
		}
	}
}

func daysUntilBirthday(birthday time.Time) int {
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	// Приводим birthday к UTC и игнорируем время (оставляем только дату)
	birthdayUTC := time.Date(birthday.Year(), birthday.Month(), birthday.Day(), 0, 0, 0, 0, time.UTC)
	birthdayThisYear := time.Date(now.Year(), birthdayUTC.Month(), birthdayUTC.Day(), 0, 0, 0, 0, time.UTC)

	if today.After(birthdayThisYear) {
		birthdayThisYear = birthdayThisYear.AddDate(1, 0, 0)
	}

	days := int(birthdayThisYear.Sub(today).Hours() / 24)

	// Если день рождения сегодня, но время еще не наступило (UTC)
	if days < 0 {
		days = 0
	}

	return days
}

func createNotificationMessage(name string, daysUntil int, date time.Time) string {
	if daysUntil == 0 {
		return fmt.Sprintf("🎉 Сегодня день рождения у %s! Поздравьте!", name)
	}
	return fmt.Sprintf("🎗️ До дня рождения %s осталось %d %s (%s)",
		name,
		daysUntil,
		formatDays(daysUntil),
		date.Format("02.01.2006"))
}

func updateLastNotifyDay(employeeID int, day int) error {
	_, err := db.Exec("UPDATE employees SET last_notify_day = ? WHERE id = ?", day, employeeID)
	return err
}

func formatDays(days int) string {
	lastDigit := days % 10
	lastTwoDigits := days % 100

	if lastTwoDigits >= 11 && lastTwoDigits <= 14 {
		return "дней"
	}
	switch lastDigit {
	case 1:
		return "день"
	case 2, 3, 4:
		return "дня"
	default:
		return "дней"
	}
}

//коммент для теста
