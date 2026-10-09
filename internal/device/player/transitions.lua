-- transitions.lua: the transition between two playlist items of PortaPixel.
--
-- portapixeld writes this file into its run directory and gives it to mpv with
-- --script. Each playlist entry carries two script options:
--
--   pptr-kind  cut | fade | crossfade | wipe-left | wipe-right | wipe-up |
--              wipe-down | push-left | push-right | push-up | push-down
--   pptr-ms    the length of the transition in milliseconds
--
-- The script uses the options of the item that ends.
--
-- How it works. When item A ends, mpv runs the hook on_unload and waits for it.
-- The hook takes a copy of the screen (screenshot-raw window) and shows it as
-- OSD overlay 62. The copy has the same pixels as the screen, so nothing
-- changes. mpv then loads item B under the copy, and the load does not show.
-- When B shows its first frame (the event playback-restart), a timer moves the
-- copy away in steps of 20 ms. At the end, the overlay goes away. The steps are
-- set by the time, not by a count: a slow device shows fewer steps, and the
-- transition keeps its length.
--
-- The kinds:
--   fade       dip to black. The copy of A goes dark. Then one black pixel,
--              scaled to the screen, goes clear over B.
--   crossfade  the copy of A goes clear over B.
--   wipe-*     the copy of A gets smaller; B shows at one side. The word is
--              the direction in which the edge moves.
--   push-*     the copy of A moves out in the direction of the word, and B
--              moves in behind it (video-pan-x and video-pan-y move B).
--
-- Rules:
--   - Every fault ends in a cut: the script removes the overlay.
--   - When B does not show in 5 s, the script removes the overlay.
--   - Show one overlay at a time. On vo=drm, two overlays on top of each other
--     mix wrongly (washed-out colours and coloured dots).
--   - A fault goes into the property user-data/pptr/fault. portapixeld reads it
--     and writes it to the ops log.
--   - The pixel work needs the FFI of LuaJIT. Without it, each transition is a
--     cut.

local has_ffi, ffi = pcall(require, "ffi")
local has_bit, bit = pcall(require, "bit")

local COVER = 62     -- the overlay that holds the copy of A
local STEP = 0.02    -- the time between two steps, in seconds
local DEADMAN = 5    -- the time that B may take to show, in seconds

local KINDS = {
    ["fade"] = true, ["crossfade"] = true,
    ["wipe-left"] = true, ["wipe-right"] = true, ["wipe-up"] = true, ["wipe-down"] = true,
    ["push-left"] = true, ["push-right"] = true, ["push-up"] = true, ["push-down"] = true,
}

-- fault tells portapixeld about a fault. The transition is then a cut.
local function fault(text)
    mp.msg.warn(text)
    mp.set_property("user-data/pptr/fault", text)
end

if not (has_ffi and has_bit) then
    fault("this mpv has no LuaJIT FFI; every transition is a cut")
    return
end

local band, bor, rshift, lshift = bit.band, bit.bor, bit.rshift, bit.lshift
local OPAQUE = -16777216 -- 0xFF000000: alpha 255 in a premultiplied BGRA pixel

local S = nil        -- the transition that runs, or nil
local deadman = nil  -- the timer that ends a transition when B does not show

local function addr(ptr)
    return string.format("&%d", tonumber(ffi.cast("uintptr_t", ptr)))
end

-- finish removes the overlay and ends the transition. why is nil when the
-- transition ended normally.
local function finish(why)
    if deadman then deadman:kill(); deadman = nil end
    if not S then return end
    if S.timer then S.timer:kill() end
    mp.commandv("overlay-remove", COVER)
    if S.pan then
        mp.set_property_number("video-pan-x", 0)
        mp.set_property_number("video-pan-y", 0)
    end
    local kind = S.kind
    S = nil
    -- The copy is 8 MB at 1080p. Give the memory back now, not at a later
    -- collection: the device can have 512 MB.
    collectgarbage()
    if why then fault(kind .. ": " .. why .. "; the transition is a cut") end
end

-- show puts a part of a picture on the screen: w x h pixels from the byte
-- offset off, at x, y.
local function show(ptr, off, x, y, w, h)
    if w < 1 or h < 1 then
        mp.commandv("overlay-remove", COVER)
        return true
    end
    return mp.commandv("overlay-add", COVER, x, y, addr(ptr), off, "bgra", w, h, S.stride)
end

-- black shows one black pixel with the alpha a (0 to 255), scaled to the
-- screen. It replaces the copy in the same overlay, so the change is one step
-- and there is never a frame with no cover. mpv copies the pixel at each call,
-- so one buffer is enough.
local pixel = ffi.new("int32_t[1]")
local function black(a)
    pixel[0] = lshift(a, 24)
    return mp.commandv("overlay-add", COVER, 0, 0, addr(pixel), 0, "bgra", 1, 1, 4, S.w, S.h)
end

-- scale writes src * a / 256 into dst for all four channels of n premultiplied
-- pixels. orm is ORed into each pixel; OPAQUE keeps the result opaque.
local function scale(src8, dst, n, a, orm)
    local src = ffi.cast("const int32_t *", src8)
    for i = 0, n - 1 do
        local px = src[i]
        local rb = band(px, 0x00FF00FF) * a
        local ga = band(rshift(px, 8), 0x00FF00FF) * a
        dst[i] = bor(band(rshift(rb, 8), 0x00FF00FF), band(ga, -16711936), orm)
    end
end

-- pan moves B. The value is in pixels of the screen; video-pan takes a part of
-- the size of the video on the screen.
local function pan(x, y)
    mp.set_property_number("video-pan-x", x / S.vw)
    mp.set_property_number("video-pan-y", y / S.vh)
end

-- step draws the transition at the time now.
local function step()
    local ok, err = pcall(function()
        local p = (mp.get_time() - S.t0) / (S.ms / 1000)
        if p >= 1 then finish() return end
        local W, H, R, k = S.w, S.h, S.stride, S.kind
        local dx, dy = math.floor(W * p), math.floor(H * p)
        local r
        if k == "fade" then
            if p < 0.5 then
                scale(S.base, S.buf, W * H, math.floor((1 - 2 * p) * 256 + 0.5), OPAQUE)
                r = show(S.buf, 0, 0, 0, W, H)
            else
                r = black(math.floor((1 - p) * 510 + 0.5))
            end
        elseif k == "crossfade" then
            scale(S.base, S.buf, W * H, math.floor((1 - p) * 256 + 0.5), 0)
            r = show(S.buf, 0, 0, 0, W, H)
        elseif k == "wipe-left" then r = show(S.base, 0, 0, 0, W - dx, H)
        elseif k == "wipe-right" then r = show(S.base, dx * 4, dx, 0, W - dx, H)
        elseif k == "wipe-up" then r = show(S.base, 0, 0, 0, W, H - dy)
        elseif k == "wipe-down" then r = show(S.base, dy * R, 0, dy, W, H - dy)
        -- A push moves B first and the copy second. In the other order, one
        -- frame can show the gap between them.
        elseif k == "push-left" then
            pan(W - dx, 0)
            r = show(S.base, dx * 4, 0, 0, W - dx, H)
        elseif k == "push-right" then
            pan(dx - W, 0)
            r = show(S.base, 0, dx, 0, W - dx, H)
        elseif k == "push-up" then
            pan(0, H - dy)
            r = show(S.base, dy * R, 0, 0, W, H - dy)
        elseif k == "push-down" then
            pan(0, dy - H)
            r = show(S.base, 0, 0, dy, W, H - dy)
        end
        if not r then finish("the overlay failed") end
    end)
    if not ok then finish("error: " .. tostring(err)) end
end

-- start begins the movement when B shows its first frame.
local function start()
    if deadman then deadman:kill(); deadman = nil end
    S.t0 = mp.get_time()
    local k = S.kind
    if k == "fade" or k == "crossfade" then
        S.buf = ffi.new("int32_t[?]", S.w * S.h)
    end
    if k:sub(1, 5) == "push-" then
        local d = mp.get_property_native("osd-dimensions")
        S.vw = math.max(1, d.w - d.ml - d.mr)
        S.vh = math.max(1, d.h - d.mt - d.mb)
        S.pan = true
    end
    S.timer = mp.add_periodic_timer(STEP, step)
    step()
end

-- cover puts the copy of the screen on top before mpv unloads item A.
local function cover()
    local kind = mp.get_opt("pptr-kind") or "cut"
    if S then
        if not S.t0 then
            -- This item showed no frame: it did not load. The copy still shows
            -- the last frame that did show, so it stays for the next item.
            return
        end
        finish() -- the item was shorter than its transition
    end
    local ms = tonumber(mp.get_opt("pptr-ms") or "") or 0
    if kind == "cut" or ms <= 0 then return end
    if not KINDS[kind] then
        fault(kind .. ": this kind is not known; the transition is a cut")
        return
    end
    local r = mp.command_native({ "screenshot-raw", "window", "bgra" })
    if type(r) ~= "table" or not r.data or r.w < 1 or r.h < 1 or r.stride ~= r.w * 4 then
        fault(kind .. ": the copy of the screen failed; the transition is a cut")
        return
    end
    S = { kind = kind, ms = ms, w = r.w, h = r.h, stride = r.stride, data = r.data }
    S.base = ffi.cast("const uint8_t *", S.data)
    if S.base[3] ~= 255 then
        -- The copy must be opaque, or B shows through it while it loads.
        local own = ffi.new("int32_t[?]", S.w * S.h)
        local src = ffi.cast("const int32_t *", S.base)
        for i = 0, S.w * S.h - 1 do own[i] = bor(src[i], OPAQUE) end
        S.own = own
        S.base = ffi.cast("const uint8_t *", own)
    end
    if not show(S.base, 0, 0, 0, S.w, S.h) then
        finish("the overlay failed")
        return
    end
    deadman = mp.add_timeout(DEADMAN, function()
        finish("the next item did not show in " .. DEADMAN .. " s")
    end)
end

mp.add_hook("on_unload", 50, function()
    local ok, err = pcall(cover)
    if ok then return end
    if S then
        finish("error: " .. tostring(err))
    else
        fault("error: " .. tostring(err) .. "; the transition is a cut")
    end
end)

mp.register_event("playback-restart", function()
    if S and not S.t0 then
        local ok, err = pcall(start)
        if not ok then finish("error: " .. tostring(err)) end
    end
end)
