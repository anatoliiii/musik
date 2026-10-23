package playback

import "time"

func WeekdayKind(day time.Weekday) string {
	want := (int(day) + 6) % 7 // Monday=0, same as the mix contract
	for _, mix := range Mixes {
		if mix.Weekday != nil && *mix.Weekday == want {
			return mix.Kind
		}
	}
	return ""
}

func TodayWeekdayKind() string {
	return WeekdayKind(time.Now().Weekday())
}
