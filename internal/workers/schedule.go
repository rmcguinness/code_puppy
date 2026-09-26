package workers

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// Schedule is when a worker runs.
type Schedule struct {
	// Text is the schedule as written.
	Text string
	// Cron is how it was understood: a five-field cron expression, or a
	// descriptor ("@hourly", "@every 90m").
	Cron     string
	Location *time.Location
	sched    cron.Schedule
}

// Next is the first run after t.
func (s Schedule) Next(t time.Time) time.Time { return s.sched.Next(t.In(s.Location)) }

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// ParseSchedule understands a cron expression, a descriptor, or plain text
// ("Every two hours", "Daily at 6 AM", "Weekdays at 9:30", "Every Monday at
// 8 PM") in the time zone named tz ("" for the machine's). Plain text is
// translated by fixed rules, never guessed: anything else is an error.
func ParseSchedule(text, tz string) (Schedule, error) {
	loc := time.Local
	if tz != "" {
		var err error
		if loc, err = time.LoadLocation(tz); err != nil {
			return Schedule{}, fmt.Errorf("unknown time zone %q", tz)
		}
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return Schedule{}, fmt.Errorf("no schedule")
	}
	expr := text
	if !looksLikeCron(text) {
		var err error
		if expr, err = plainToCron(text); err != nil {
			return Schedule{}, err
		}
	}
	sched, err := cronParser.Parse(expr)
	if err != nil {
		return Schedule{}, fmt.Errorf("schedule %q: %w", text, err)
	}
	return Schedule{Text: text, Cron: expr, Location: loc, sched: sched}, nil
}

// looksLikeCron: a descriptor, or five fields of cron characters.
func looksLikeCron(s string) bool {
	if strings.HasPrefix(s, "@") {
		return true
	}
	fields := strings.Fields(s)
	if len(fields) != 5 {
		return false
	}
	for _, f := range fields {
		if strings.Trim(f, "0123456789*/,-?LW#ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz") != "" || !strings.ContainsAny(f, "0123456789*") {
			return false
		}
	}
	return true
}

var numbers = map[string]int{
	"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8,
	"nine": 9, "ten": 10, "eleven": 11, "twelve": 12, "fifteen": 15, "twenty": 20, "thirty": 30,
}

var weekdays = map[string]string{
	"sunday": "0", "monday": "1", "tuesday": "2", "wednesday": "3", "thursday": "4", "friday": "5", "saturday": "6",
	"sun": "0", "mon": "1", "tue": "2", "tues": "2", "wed": "3", "thu": "4", "thur": "4", "thurs": "4", "fri": "5", "sat": "6",
}

var (
	reEvery = regexp.MustCompile(`^every (?:(\w+) )?(minute|hour|day)s?$`)
	reAt    = regexp.MustCompile(`^(daily|every day|each day|weekdays|every weekday|weekends|every weekend|(?:every |on )?(\w+?)s?) at (.+)$`)
	reTime  = regexp.MustCompile(`^(\d{1,2})(?::(\d{2}))?\s*(am|pm|a\.m\.?|p\.m\.?)?$`)
)

// plainToCron translates the plain-text schedules it knows into cron.
func plainToCron(text string) (string, error) {
	s := strings.ToLower(strings.Join(strings.Fields(text), " "))
	s = strings.TrimSuffix(s, ".")
	switch s {
	case "hourly", "every hour":
		return "@hourly", nil
	case "daily", "every day", "each day":
		return "@daily", nil
	case "weekly", "every week":
		return "@weekly", nil
	case "monthly", "every month":
		return "@monthly", nil
	}
	if m := reEvery.FindStringSubmatch(s); m != nil {
		n := 1
		if m[1] != "" {
			var ok bool
			if n, ok = number(m[1]); !ok || n < 1 {
				return "", unknown(text)
			}
		}
		switch m[2] {
		case "minute":
			if n >= 60 || 60%n != 0 {
				return fmt.Sprintf("@every %dm", n), nil
			}
			return fmt.Sprintf("*/%d * * * *", n), nil
		case "hour":
			if n >= 24 || 24%n != 0 {
				return fmt.Sprintf("@every %dh", n), nil
			}
			return fmt.Sprintf("0 */%d * * *", n), nil
		case "day":
			return fmt.Sprintf("0 0 */%d * *", n), nil
		}
	}
	if m := reAt.FindStringSubmatch(s); m != nil {
		hour, minute, err := clock(m[3])
		if err != nil {
			return "", fmt.Errorf("schedule %q: %w", text, err)
		}
		days := "*"
		switch m[1] {
		case "daily", "every day", "each day":
		case "weekdays", "every weekday":
			days = "1-5"
		case "weekends", "every weekend":
			days = "0,6"
		default:
			d, ok := weekdays[m[2]]
			if !ok {
				return "", unknown(text)
			}
			days = d
		}
		return fmt.Sprintf("%d %d * * %s", minute, hour, days), nil
	}
	return "", unknown(text)
}

func unknown(text string) error {
	return fmt.Errorf(`schedule %q isn't understood: use cron ("0 6 * * *"), a descriptor ("@daily", "@every 90m"), or phrases like "every 2 hours", "daily at 6 AM", "weekdays at 9:30", "every Monday at 8 PM"`, text)
}

func number(s string) (int, bool) {
	if n, err := strconv.Atoi(s); err == nil {
		return n, true
	}
	n, ok := numbers[s]
	return n, ok
}

// clock reads "6", "6 am", "6:30 PM", "18:30", "noon" or "midnight".
func clock(s string) (hour, minute int, err error) {
	switch s {
	case "noon":
		return 12, 0, nil
	case "midnight":
		return 0, 0, nil
	}
	m := reTime.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, fmt.Errorf("%q isn't a time of day", s)
	}
	hour, _ = strconv.Atoi(m[1])
	if m[2] != "" {
		minute, _ = strconv.Atoi(m[2])
	}
	switch strings.ReplaceAll(m[3], ".", "") {
	case "am":
		if hour < 1 || hour > 12 {
			return 0, 0, fmt.Errorf("%q isn't a time of day", s)
		}
		if hour == 12 {
			hour = 0
		}
	case "pm":
		if hour < 1 || hour > 12 {
			return 0, 0, fmt.Errorf("%q isn't a time of day", s)
		}
		if hour != 12 {
			hour += 12
		}
	}
	if hour > 23 || minute > 59 {
		return 0, 0, fmt.Errorf("%q isn't a time of day", s)
	}
	return hour, minute, nil
}
