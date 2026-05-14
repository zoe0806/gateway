package tools

//内存缓存
var allowedApiKeys = make(map[string]bool)

func init() {
	//todo:从数据库中获取
	allowedApiKeys["sk-proj-1234567890"] = true
}

func IsAllowedApiKey(apiKey string) bool {
	_, ok := allowedApiKeys[apiKey]
	return ok
}

func AddApiKey(apiKey string) {
	allowedApiKeys[apiKey] = true
}

func RemoveApiKey(apiKey string) {
	delete(allowedApiKeys, apiKey)
}
