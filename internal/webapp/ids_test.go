package webapp

// Синтетические Telegram ID и группа для тестов панели. Боевые значения в код не
// кладём: в проде вайтлист и админ приходят из .env (WHITELIST, ADMIN_TELEGRAM_ID).
const (
	testAdminID   int64 = 700000000
	testGroupID   int64 = 100000001
	testGroupCode       = "TEST-GROUP"
)

// testWhitelist: первый элемент — админ, дальше «кенты» для проверок доступа.
var testWhitelist = []int64{testAdminID, 600000001, 600000002, 600000003, 600000004}
