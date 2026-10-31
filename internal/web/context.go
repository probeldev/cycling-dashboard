package web

const maxNotesLen = 2000 // символов (рун)

// rpeOption — вариант RPE с подписью для селекта.
type rpeOption struct {
	N     int
	Label string
}

// rpeOptions — шкала RPE 1–10.
var rpeOptions = []rpeOption{
	{1, "очень легко"},
	{2, "легко"},
	{3, "умеренно"},
	{4, "комфортно тяжело"},
	{5, "тяжело"},
	{6, "довольно тяжело"},
	{7, "очень тяжело"},
	{8, "жёстко"},
	{9, "почти максимум"},
	{10, "максимум"},
}
