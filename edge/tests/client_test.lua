local h = require "helpers"
local client = require "client"

local T = {}

function T.test_base_url()
  h.assert_equal(client.base_url({ serverAddress = "192.168.1.50", serverPort = 8580 }), "http://192.168.1.50:8580")
  h.assert_equal(client.base_url({ serverAddress = "ps5.lan" }), "http://ps5.lan:8580")
  h.assert_equal(client.base_url({ serverAddress = "10.0.0.5", serverPort = "9000" }), "http://10.0.0.5:9000")
  h.assert_nil(client.base_url({ serverAddress = "" }))
  h.assert_nil(client.base_url(nil))
end

function T.test_classify()
  h.assert_nil(client.classify(200))
  h.assert_nil(client.classify(204))
  h.assert_equal(client.classify(409), "unconfigured")
  h.assert_equal(client.classify(423), "busy")
  h.assert_equal(client.classify(501), "unsupported")
  h.assert_equal(client.classify(504), "timeout")
  h.assert_equal(client.classify(404), "incompatible")
  h.assert_equal(client.classify(500), "error")
  h.assert_equal(client.classify(502), "error")
  h.assert_equal(client.classify("timeout"), "unreachable")
  h.assert_equal(client.classify(nil), "unreachable")
end

function T.test_get_status_decodes_body()
  local http = h.http({ { code = 200, body = '{"state":"standby","code":620,"host":"192.168.1.20"}' } })
  local ok, body, kind = client.get_status(h.device(), h.deps(http))
  h.assert_true(ok)
  h.assert_nil(kind)
  h.assert_equal(body.state, "standby")
  local req = http.requests[1]
  h.assert_equal(req.url, "http://192.168.1.50:8580/status")
  h.assert_equal(req.method, "GET")
  h.assert_contains(req.headers["User-Agent"], "smartthings-ps5-control-edge/")
end

function T.test_wake_posts_with_long_timeout()
  local http = h.http({ { code = 200, body = '{"outcome":"success","status_text":"Success","attempts":1}' } })
  local seen_timeout
  local deps = h.deps(http)
  -- A module-shaped http records the TIMEOUT the client sets on it.
  deps.http = setmetatable({}, {
    __newindex = function(t, k, v)
      if k == "TIMEOUT" then seen_timeout = v end
      rawset(t, k, v)
    end,
  })
  deps.http.request = http.request
  local ok, body = client.wake(h.device(), deps)
  h.assert_true(ok)
  h.assert_equal(body.outcome, "success")
  h.assert_equal(http.requests[1].method, "POST")
  h.assert_equal(http.requests[1].url, "http://192.168.1.50:8580/wake")
  h.assert_equal(http.requests[1].headers["Content-Length"], "0")
  h.assert_equal(seen_timeout, client.WAKE_TIMEOUT)
end

function T.test_error_body_comes_back_with_kind()
  local http = h.http({ { code = 504, body = '{"outcome":"timeout","status_text":"Page Timeout","attempts":3}' } })
  local ok, body, kind = client.wake(h.device(), h.deps(http))
  h.assert_false(ok)
  h.assert_equal(kind, "timeout")
  h.assert_equal(body.status_text, "Page Timeout")
end

function T.test_transport_failure_is_unreachable()
  local http = h.http({ { err = "timeout" } })
  local ok, body, kind = client.get_status(h.device(), h.deps(http))
  h.assert_false(ok)
  h.assert_nil(body)
  h.assert_equal(kind, "unreachable")

  local deps = h.deps(nil)
  deps.http = function() error("socket exploded") end
  ok, _, kind = client.get_status(h.device(), deps)
  h.assert_false(ok)
  h.assert_equal(kind, "unreachable")
end

function T.test_empty_address_is_unconfigured_without_a_request()
  local http = h.http({ { code = 200, body = "{}" } })
  local ok, _, kind = client.get_status(h.device({ serverAddress = "" }), h.deps(http))
  h.assert_false(ok)
  h.assert_equal(kind, "unconfigured")
  h.assert_equal(#http.requests, 0)
end

function T.test_non_json_200_is_incompatible()
  local http = h.http({ { code = 200, body = "<html>captive portal</html>" } })
  local ok, _, kind = client.get_status(h.device(), h.deps(http))
  h.assert_false(ok)
  h.assert_equal(kind, "incompatible")
end

return T
