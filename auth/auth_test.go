package auth

import (
	"testing"

	"github.com/jsyzchen/pan/conf"
)

func TestAuth_OAuthUrl(t *testing.T) {
	authClient := NewAuthClient(conf.TestData.ClientID, conf.TestData.ClientSecret)
	res := authClient.OAuthUrl(conf.TestData.RedirectUri)
	t.Logf("TestAuth_OAuthUrl res: %+v", res)
}

func TestAuth_DeviceCode(t *testing.T) {
	authClient := NewAuthClient(conf.TestData.ClientID, conf.TestData.ClientSecret)
	res, err := authClient.DeviceCode()
	if err != nil {
		t.Errorf("authClient.DeviceCode failed, err:%v", err)
	}
	t.Logf("TestAuth_DeviceCode res: %+v", res)
}

func TestAuth_AccessTokenByAuthCode(t *testing.T) {
	authClient := NewAuthClient(conf.TestData.ClientID, conf.TestData.ClientSecret)
	res, err := authClient.AccessTokenByAuthCode(conf.TestData.Code, conf.TestData.RedirectUri)
	if err != nil {
		t.Errorf("authClient.AccessTokenByAuthCode failed, err:%v", err)
	}
	t.Logf("TestAuth_AccessTokenByAuthCode res: %+v", res)
}

func TestAuth_AccessTokenByDeviceCode(t *testing.T) {
	authClient := NewAuthClient(conf.TestData.ClientID, conf.TestData.ClientSecret)
	res, err := authClient.AccessTokenByDeviceCode(conf.TestData.DeviceCode)
	if err != nil {
		t.Errorf("authClient.AccessTokenByDeviceCode failed, err:%v", err)
	}
	t.Logf("TestAuth_AccessTokenByDeviceCode res: %+v", res)
}

func TestAuth_RefreshToken(t *testing.T) {
	authClient := NewAuthClient(conf.TestData.ClientID, conf.TestData.ClientSecret)
	res, err := authClient.RefreshToken(conf.TestData.RefreshToken)
	if err != nil {
		t.Errorf("authClient.AccessToken failed, err:%v", err)
	}
	t.Logf("TestAuth_RefreshToken res:%+v", res)
}

func TestAuth_UserInfo(t *testing.T) {
	authClient := NewAuthClient(conf.TestData.ClientID, conf.TestData.ClientSecret)
	res, err := authClient.UserInfo(conf.TestData.AccessToken)
	if err != nil {
		t.Errorf("TestAuth_UserInfo failed, err:%v", err)
	}
	t.Logf("TestAuth_UserInfo res:%+v", res)
}
