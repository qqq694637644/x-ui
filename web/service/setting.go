package service

import (
	_ "embed"
	"errors"
	"fmt"
	"net"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
	"x-ui/database"
	"x-ui/database/model"
	"x-ui/logger"
	"x-ui/util/common"
	"x-ui/util/random"
	"x-ui/util/reflect_util"
	"x-ui/web/entity"

	"gorm.io/gorm"
)

//go:embed config.json
var xrayTemplateConfig string

var defaultValueMap = map[string]string{
	"xrayTemplateConfig":   xrayTemplateConfig,
	"webListen":            "",
	"webPort":              "54321",
	"webCertFile":          "",
	"webKeyFile":           "",
	"secret":               random.Seq(32),
	"webBasePath":          "/",
	"timeLocation":         "Asia/Shanghai",
	"tgBotEnable":          "false",
	"tgBotToken":           "",
	"tgBotChatId":          "0",
	"tgRunTime":            "",
	"caddyPath":            "/opt/caddy",
	"subscriptionEnable":   "false",
	"subscriptionToken":    "",
	"subscriptionBaseUrl":  "",
	"publicBaseDomain":     "",
	"publicPort":           "443",
	"hostRandomLength":     "10",
	"endpointDrainSeconds": "1800",
	"caddyTlsCertFile":     "",
	"caddyTlsKeyFile":      "",
}

type SettingService struct {
}

func (s *SettingService) GetAllSetting() (*entity.AllSetting, error) {
	db := database.GetDB()
	settings := make([]*model.Setting, 0)
	err := db.Model(model.Setting{}).Find(&settings).Error
	if err != nil {
		return nil, err
	}
	allSetting := &entity.AllSetting{}
	t := reflect.TypeOf(allSetting).Elem()
	v := reflect.ValueOf(allSetting).Elem()
	fields := reflect_util.GetFields(t)

	setSetting := func(key, value string) (err error) {
		defer func() {
			panicErr := recover()
			if panicErr != nil {
				err = errors.New(fmt.Sprint(panicErr))
			}
		}()

		var found bool
		var field reflect.StructField
		for _, f := range fields {
			if f.Tag.Get("json") == key {
				field = f
				found = true
				break
			}
		}

		if !found {
			// 有些设置自动生成，不需要返回到前端给用户修改
			return nil
		}

		fieldV := v.FieldByName(field.Name)
		switch t := fieldV.Interface().(type) {
		case int:
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return err
			}
			fieldV.SetInt(n)
		case string:
			fieldV.SetString(value)
		case bool:
			fieldV.SetBool(value == "true")
		default:
			return common.NewErrorf("unknown field %v type %v", key, t)
		}
		return
	}

	keyMap := map[string]bool{}
	for _, setting := range settings {
		err := setSetting(setting.Key, setting.Value)
		if err != nil {
			return nil, err
		}
		keyMap[setting.Key] = true
	}

	for key, value := range defaultValueMap {
		if keyMap[key] {
			continue
		}
		err := setSetting(key, value)
		if err != nil {
			return nil, err
		}
	}

	return allSetting, nil
}

func (s *SettingService) ResetSettings() error {
	db := database.GetDB()
	return db.Where("1 = 1").Delete(model.Setting{}).Error
}

func (s *SettingService) getSetting(key string) (*model.Setting, error) {
	db := database.GetDB()
	setting := &model.Setting{}
	err := db.Model(model.Setting{}).Where("key = ?", key).First(setting).Error
	if err != nil {
		return nil, err
	}
	return setting, nil
}

func (s *SettingService) saveSetting(key string, value string) error {
	setting, err := s.getSetting(key)
	db := database.GetDB()
	if database.IsNotFound(err) {
		return db.Create(&model.Setting{
			Key:   key,
			Value: value,
		}).Error
	} else if err != nil {
		return err
	}
	setting.Key = key
	setting.Value = value
	return db.Save(setting).Error
}

func (s *SettingService) getString(key string) (string, error) {
	setting, err := s.getSetting(key)
	if database.IsNotFound(err) {
		value, ok := defaultValueMap[key]
		if !ok {
			return "", common.NewErrorf("key <%v> not in defaultValueMap", key)
		}
		return value, nil
	} else if err != nil {
		return "", err
	}
	return setting.Value, nil
}

func (s *SettingService) setString(key string, value string) error {
	return s.saveSetting(key, value)
}

func (s *SettingService) getBool(key string) (bool, error) {
	str, err := s.getString(key)
	if err != nil {
		return false, err
	}
	return strconv.ParseBool(str)
}

func (s *SettingService) setBool(key string, value bool) error {
	return s.setString(key, strconv.FormatBool(value))
}

func (s *SettingService) getInt(key string) (int, error) {
	str, err := s.getString(key)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(str)
}

func (s *SettingService) setInt(key string, value int) error {
	return s.setString(key, strconv.Itoa(value))
}

func (s *SettingService) GetXrayConfigTemplate() (string, error) {
	return s.getString("xrayTemplateConfig")
}

func (s *SettingService) GetCaddyPath() (string, error) {
	return s.getString("caddyPath")
}

func (s *SettingService) SetCaddyPath(path string) error {
	return s.setString("caddyPath", path)
}

func (s *SettingService) GetEndpointSettings() (*entity.EndpointSettings, error) {
	token, err := s.GetSubscriptionToken()
	if err != nil {
		return nil, err
	}
	enabled, err := s.getBool("subscriptionEnable")
	if err != nil {
		return nil, err
	}
	subscriptionBaseURL, err := s.getString("subscriptionBaseUrl")
	if err != nil {
		return nil, err
	}
	domain, err := s.getString("publicBaseDomain")
	if err != nil {
		return nil, err
	}
	port, err := s.getInt("publicPort")
	if err != nil {
		return nil, err
	}
	hostLength, err := s.getInt("hostRandomLength")
	if err != nil {
		return nil, err
	}
	drainSeconds, err := s.getInt("endpointDrainSeconds")
	if err != nil {
		return nil, err
	}
	certFile, err := s.getString("caddyTlsCertFile")
	if err != nil {
		return nil, err
	}
	keyFile, err := s.getString("caddyTlsKeyFile")
	if err != nil {
		return nil, err
	}
	return &entity.EndpointSettings{
		SubscriptionEnable:   enabled,
		SubscriptionToken:    token,
		SubscriptionBaseURL:  subscriptionBaseURL,
		PublicBaseDomain:     domain,
		PublicPort:           port,
		HostRandomLength:     hostLength,
		EndpointDrainSeconds: drainSeconds,
		CaddyTLSCertFile:     certFile,
		CaddyTLSKeyFile:      keyFile,
	}, nil
}

func (s *SettingService) UpdateEndpointSettings(settings *entity.EndpointSettings) error {
	settings.PublicBaseDomain = normalizeDomain(settings.PublicBaseDomain)
	normalizedSubscriptionBaseURL, err := normalizeSubscriptionBaseURL(settings.SubscriptionBaseURL)
	if err != nil {
		return err
	}
	settings.SubscriptionBaseURL = normalizedSubscriptionBaseURL
	settings.CaddyTLSCertFile = strings.TrimSpace(settings.CaddyTLSCertFile)
	settings.CaddyTLSKeyFile = strings.TrimSpace(settings.CaddyTLSKeyFile)
	if settings.PublicBaseDomain != "" && !validDomain(settings.PublicBaseDomain) {
		return common.NewError("公网基础域名不合法: ", settings.PublicBaseDomain)
	}
	if settings.PublicPort <= 0 || settings.PublicPort > 65535 {
		return common.NewError("公网端口不合法: ", settings.PublicPort)
	}
	if settings.SubscriptionEnable && settings.SubscriptionBaseURL == "" {
		return common.NewError("启用客户端订阅时必须配置稳定订阅基础 URL")
	}
	if settings.SubscriptionBaseURL != "" {
		u, _ := url.Parse(settings.SubscriptionBaseURL)
		if hostBelongsToManagedZone(u.Hostname(), settings.PublicBaseDomain) {
			return common.NewError("稳定订阅域名不得属于托管基础域名区域: ", u.Hostname())
		}
	}
	if settings.HostRandomLength < 4 || settings.HostRandomLength > 32 {
		return common.NewError("随机子域名长度必须在 4 到 32 之间")
	}
	if settings.EndpointDrainSeconds < 0 || settings.EndpointDrainSeconds > 7*24*60*60 {
		return common.NewError("旧入口保留时间必须在 0 到 604800 秒之间")
	}
	if (settings.CaddyTLSCertFile == "") != (settings.CaddyTLSKeyFile == "") {
		return common.NewError("Caddy TLS 证书与私钥路径必须同时填写或同时留空")
	}
	pairs := map[string]string{
		"subscriptionEnable":   strconv.FormatBool(settings.SubscriptionEnable),
		"subscriptionBaseUrl":  settings.SubscriptionBaseURL,
		"publicBaseDomain":     settings.PublicBaseDomain,
		"publicPort":           strconv.Itoa(settings.PublicPort),
		"hostRandomLength":     strconv.Itoa(settings.HostRandomLength),
		"endpointDrainSeconds": strconv.Itoa(settings.EndpointDrainSeconds),
		"caddyTlsCertFile":     settings.CaddyTLSCertFile,
		"caddyTlsKeyFile":      settings.CaddyTLSKeyFile,
	}
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		for key, value := range pairs {
			stored := &model.Setting{}
			err := tx.Where("key = ?", key).First(stored).Error
			if database.IsNotFound(err) {
				if err := tx.Create(&model.Setting{Key: key, Value: value}).Error; err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			stored.Value = value
			if err := tx.Save(stored).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func normalizeSubscriptionBaseURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	u, err := url.Parse(value)
	if err != nil {
		return "", common.NewError("稳定订阅基础 URL 不合法: ", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", common.NewError("稳定订阅基础 URL 仅支持 http 或 https")
	}
	if u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", common.NewError("稳定订阅基础 URL 必须是无用户信息、查询参数和 fragment 的绝对 URL")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return strings.TrimRight(u.String(), "/"), nil
}

func requestHostname(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		return strings.ToLower(strings.Trim(host, "[]."))
	}
	return strings.ToLower(strings.Trim(value, "[]."))
}

func hostBelongsToManagedZone(host string, baseDomain string) bool {
	host = strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]."))
	baseDomain = normalizeDomain(baseDomain)
	if host == "" || baseDomain == "" {
		return false
	}
	return host == baseDomain || strings.HasSuffix(host, "."+baseDomain)
}

func (s *SettingService) GetSubscriptionToken() (string, error) {
	token, err := s.getString("subscriptionToken")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(token) != "" {
		return token, nil
	}
	return s.RegenerateSubscriptionToken()
}

func (s *SettingService) RegenerateSubscriptionToken() (string, error) {
	token, err := random.SecureToken(32)
	if err != nil {
		return "", err
	}
	if err := s.saveSetting("subscriptionToken", token); err != nil {
		return "", err
	}
	return token, nil
}

func normalizeDomain(value string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(value), "."))
}

func validDomain(value string) bool {
	if value == "" || len(value) > 253 {
		return false
	}
	labels := strings.Split(value, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func (s *SettingService) GetListen() (string, error) {
	return s.getString("webListen")
}

func (s *SettingService) GetTgBotToken() (string, error) {
	return s.getString("tgBotToken")
}

func (s *SettingService) SetTgBotToken(token string) error {
	return s.setString("tgBotToken", token)
}

func (s *SettingService) GetTgBotChatId() (int, error) {
	return s.getInt("tgBotChatId")
}

func (s *SettingService) SetTgBotChatId(chatId int) error {
	return s.setInt("tgBotChatId", chatId)
}

func (s *SettingService) SetTgbotenabled(value bool) error {
	return s.setBool("tgBotEnable", value)
}

func (s *SettingService) GetTgbotenabled() (bool, error) {
	return s.getBool("tgBotEnable")
}

func (s *SettingService) SetTgbotRuntime(time string) error {
	return s.setString("tgRunTime", time)
}

func (s *SettingService) GetTgbotRuntime() (string, error) {
	return s.getString("tgRunTime")
}

func (s *SettingService) GetPort() (int, error) {
	return s.getInt("webPort")
}

func (s *SettingService) SetPort(port int) error {
	return s.setInt("webPort", port)
}

func (s *SettingService) GetCertFile() (string, error) {
	return s.getString("webCertFile")
}

func (s *SettingService) GetKeyFile() (string, error) {
	return s.getString("webKeyFile")
}

func (s *SettingService) GetSecret() ([]byte, error) {
	secret, err := s.getString("secret")
	if secret == defaultValueMap["secret"] {
		err := s.saveSetting("secret", secret)
		if err != nil {
			logger.Warning("save secret failed:", err)
		}
	}
	return []byte(secret), err
}

func (s *SettingService) GetBasePath() (string, error) {
	basePath, err := s.getString("webBasePath")
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}
	if !strings.HasSuffix(basePath, "/") {
		basePath += "/"
	}
	return basePath, nil
}

func (s *SettingService) GetTimeLocation() (*time.Location, error) {
	l, err := s.getString("timeLocation")
	if err != nil {
		return nil, err
	}
	location, err := time.LoadLocation(l)
	if err != nil {
		defaultLocation := defaultValueMap["timeLocation"]
		logger.Errorf("location <%v> not exist, using default location: %v", l, defaultLocation)
		return time.LoadLocation(defaultLocation)
	}
	return location, nil
}

func (s *SettingService) UpdateAllSetting(allSetting *entity.AllSetting) error {
	if err := allSetting.CheckValid(); err != nil {
		return err
	}

	v := reflect.ValueOf(allSetting).Elem()
	t := reflect.TypeOf(allSetting).Elem()
	fields := reflect_util.GetFields(t)
	errs := make([]error, 0)
	for _, field := range fields {
		key := field.Tag.Get("json")
		fieldV := v.FieldByName(field.Name)
		value := fmt.Sprint(fieldV.Interface())
		err := s.saveSetting(key, value)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return common.Combine(errs...)
}
