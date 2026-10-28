-- Minimal cosock stub.
--
-- UDP sockets record what they were asked to send, and hand back whatever a
-- test queued in `inbox` from `receivefrom`. `asyncify` raises on purpose:
-- HTTP in tests must go through an injected `deps.http`, never a real socket.

local cosock = { socket = {}, sent = {}, inbox = {} }

function cosock.reset()
  cosock.sent = {}
  cosock.inbox = {}
end

function cosock.socket.udp()
  local sock = { options = {}, timeout = nil, sent = {}, closed = false, inbox = cosock.inbox }

  function sock:setoption(name, value)
    self.options[name] = value
    return 1
  end

  function sock:setsockname(ip, port)
    self.bound = { ip = ip, port = port }
    return 1
  end

  function sock:settimeout(seconds)
    self.timeout = seconds
    return 1
  end

  function sock:sendto(data, ip, port)
    local record = { data = data, ip = ip, port = port }
    self.sent[#self.sent + 1] = record
    cosock.sent[#cosock.sent + 1] = record
    return 1
  end

  --- Each queued item is `{ data = ..., ip = ..., port = ... }`; an empty
  --- queue times out like a real socket.
  function sock:receivefrom()
    local item = table.remove(self.inbox, 1)
    if not item then
      return nil, "timeout"
    end
    return item.data, item.ip, item.port
  end

  function sock:close()
    self.closed = true
    return 1
  end

  return sock
end

function cosock.asyncify(name)
  error("cosock.asyncify(" .. tostring(name) .. ") is not available in tests; inject deps.http", 0)
end

function cosock.spawn(fn, name) -- luacheck: ignore name
  return fn()
end

return cosock
