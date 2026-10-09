-- transitions.lua: the transition between two playlist items of PortaPixel.
--
-- portapixeld writes this file into its run directory and gives it to mpv with
-- --script. Each playlist entry carries these script options:
--
--   pptr-kind  the transition into the NEXT item (the words of config.Transitions):
--              cut | fade | fade-white | crossfade | wipe-* | push-* | slide-in-* |
--              slide-out-* | zoom-out | split. The * is left, right, up or down.
--   pptr-ms    the length of that transition in milliseconds
--   pptr-kb    Ken Burns, for an image only: the seconds that the image shows
--
-- The script uses pptr-kind and pptr-ms of the item that ends. The daemon puts
-- the transition of the next item there, so an item can name its own transition.
-- It uses pptr-kb of the item that shows.
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
--   fade       dip to black. The copy of A goes dark. Then a black square,
--              scaled to the screen, goes clear over B.
--   fade-white the same, through white.
--   crossfade  the copy of A goes clear over B.
--   wipe-*     the copy of A gets smaller; B shows at one side. The word is
--              the direction in which the edge moves.
--   push-*     the copy of A moves out in the direction of the word, and B
--              moves in behind it (video-pan-x and video-pan-y move B).
--   slide-in-* B moves in over the copy of A, which stays where it is. The
--              copy gets smaller under B (the same crop as wipe-*), and
--              video-pan moves B as in push-*.
--   slide-out-* the copy of A moves out in the direction of the word and B
--              stays where it is. This is the push without the move of B.
--   zoom-out   the copy of A gets smaller toward the centre of the screen
--              (the destination size of the overlay).
--   split      the copy of A is two halves. They move apart from the centre,
--              each one in its own overlay. The two overlays do not overlap.
--
-- Only fade and fade-white change pixels (a pass of LuaJIT over the copy in the
-- first half). Each other kind moves, crops or scales an overlay.
--
-- Ken Burns. An image with pptr-kb zooms and pans slowly while it shows. The
-- timer sets video-zoom, video-pan-x and video-pan-y in steps. It starts when the
-- transition into the image is over, so it does not use video-pan at the same
-- time as push-* and slide-in-*. The zoom goes in or out. The drift goes to a
-- random side, and it is small enough that the edge of the picture does not show.
-- When the image ends, the script puts the copy on top and sets zoom and pan
-- back to 0 under it. A cut after Ken Burns also holds the copy until the next
-- item shows its first frame, or the next item would show with the zoom of A.
--
-- The moving crossfade. When portapixeld sets user-data/pptr/motion to "yes",
-- a crossfade from a video A has motion on both sides. The hook on_preloaded of
-- A adds the video of B as a second track (video-add) and sets a lavfi-complex
-- graph on A: overlay puts B on A with an alpha that goes from clear to opaque
-- in the last pptr-ms of A. Before the mix, overlay passes the frames of A
-- through with no work. A stops at the end of the mix. B then starts at the
-- time that the mix reached, under the copy of the screen, and the copy goes
-- away at its first frame. If B has less than 0.1 s left after that time, B
-- starts at 0, so it does not end at once. The audio is the audio of A only.
-- The script does this only when A and B have the same shape, A is long
-- enough, and no fault happened before; else the crossfade uses the copy.
--
-- A fault of the filter graph stops the moving crossfade in this mpv: the
-- script reports it and uses the copy. mpv refuses an item whose graph fails
-- in its setup, so the script then plays A again with no graph.
--
-- After each moving crossfade the script puts its dropped frames into
-- user-data/pptr/moved. portapixeld compares them with a limit and sets
-- user-data/pptr/motion to "no" when the device is too slow.
--
-- Rules:
--   - Every fault ends in a cut: the script removes the overlay.
--   - When B does not show in 5 s, the script removes the overlay.
--   - Show one overlay at a time. On vo=drm, two overlays on top of each other
--     mix wrongly (washed-out colours and coloured dots). Only split uses two
--     overlays, and they do not touch each other.
--   - A fault goes into the property user-data/pptr/fault. portapixeld reads it
--     and writes it to the ops log.
--   - The pixel work needs the FFI of LuaJIT. Without it, each transition is a
--     cut.

local has_ffi, ffi = pcall(require, "ffi")
local has_bit, bit = pcall(require, "bit")

local COVER = 62     -- the overlay that holds the copy of A
local COVER2 = 63    -- the second overlay, for the right half in split
local STEP = 0.02    -- the time between two steps, in seconds
local DEADMAN = 5    -- the time that B may take to show, in seconds
local LEAD = 1       -- the part of A that plays before a moving crossfade, in seconds
local LATE = 0.25    -- a moving crossfade that stops this much early did not end
local LEFT = 0.1     -- an item that continues a mix needs this much of itself after it
local KB_ZOOM = 0.16    -- Ken Burns: the zoom at the far end, as a power of 2 (1.12 times)
local KB_DRIFT = 0.04   -- Ken Burns: the pan at the far end, as a part of the picture
local KB_STEP = 0.1     -- Ken Burns: the time between two steps, in seconds
local KB_MIN = 1        -- Ken Burns needs this many seconds at least

local KINDS = {
    ["fade"] = true, ["fade-white"] = true, ["crossfade"] = true,
    ["wipe-left"] = true, ["wipe-right"] = true, ["wipe-up"] = true, ["wipe-down"] = true,
    ["push-left"] = true, ["push-right"] = true, ["push-up"] = true, ["push-down"] = true,
    ["slide-in-left"] = true, ["slide-in-right"] = true, ["slide-in-up"] = true, ["slide-in-down"] = true,
    ["slide-out-left"] = true, ["slide-out-right"] = true, ["slide-out-up"] = true, ["slide-out-down"] = true,
    ["zoom-out"] = true, ["split"] = true,
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

math.randomseed(os.time() + math.floor(mp.get_time() * 1000))
local band, bor, rshift, lshift = bit.band, bit.bor, bit.rshift, bit.lshift
local OPAQUE = -16777216 -- 0xFF000000: alpha 255 in a premultiplied BGRA pixel

local S = nil        -- the transition that runs, or nil
local deadman = nil  -- the timer that ends a transition when B does not show
local P = nil        -- the moving crossfade that the item on the screen prepared
local J = nil        -- the item that continues a moving crossfade: {id, at}
local F = nil        -- a moving crossfade that did not reach its end, until end-file
local broken = false -- a moving crossfade failed; this mpv makes no more of them
local moved = 0      -- the count of moving crossfades, for user-data/pptr/moved
local kbwait = nil   -- Ken Burns of the item on the screen, until its transition is over
local kb = nil       -- Ken Burns that runs: {t0, span, timer, z0, z1, x0, x1, y0, y1}
local kbview = false -- Ken Burns changed zoom or pan, and they are not back at 0
local kbseen = false -- the item on the screen did its first playback-restart

local function addr(ptr)
    return string.format("&%d", tonumber(ffi.cast("uintptr_t", ptr)))
end

-- kb_view_reset puts zoom and pan back to 0 after Ken Burns.
local function kb_view_reset()
    if not kbview then return end
    kbview = false
    mp.set_property_number("video-zoom", 0)
    mp.set_property_number("video-pan-x", 0)
    mp.set_property_number("video-pan-y", 0)
end

-- kb_stop stops Ken Burns. The view stays as it is until kb_view_reset.
local function kb_stop()
    kbwait = nil
    if kb then
        kb.timer:kill()
        kb = nil
    end
end

-- kb_step sets the view for the time now. At the end, the view stays as it is.
local function kb_step()
    local ok, err = pcall(function()
        local t = (mp.get_time() - kb.t0) / kb.span
        if t >= 1 then
            t = 1
            kb.timer:kill()
        end
        mp.set_property_number("video-zoom", kb.z0 + (kb.z1 - kb.z0) * t)
        mp.set_property_number("video-pan-x", kb.x0 + (kb.x1 - kb.x0) * t)
        mp.set_property_number("video-pan-y", kb.y0 + (kb.y1 - kb.y0) * t)
    end)
    if not ok then
        kb_stop()
        kb_view_reset()
        fault("ken burns: error: " .. tostring(err))
    end
end

-- kb_begin starts Ken Burns for the rest of the time of the image. It is
-- called when the transition into the image is over.
local function kb_begin()
    local w = kbwait
    kbwait = nil
    if not w then return end
    local left = w.total - (mp.get_time() - w.t0)
    if left < KB_MIN then return end
    -- The path goes in or out. The drift is small, so the zoomed picture
    -- always covers the screen: the overhang at the far end is 5 % of the
    -- picture on each side, and the drift is 4 % at most.
    local function side() return math.random() < 0.5 and -1 or 1 end
    local dx = (0.4 + 0.6 * math.random()) * KB_DRIFT * side()
    local dy = (0.4 + 0.6 * math.random()) * KB_DRIFT * side()
    local zoom_in = math.random() < 0.5
    kb = { t0 = mp.get_time(), span = left,
        z0 = zoom_in and 0 or KB_ZOOM, z1 = zoom_in and KB_ZOOM or 0,
        x0 = zoom_in and 0 or dx, x1 = zoom_in and dx or 0,
        y0 = zoom_in and 0 or dy, y1 = zoom_in and dy or 0 }
    kbview = true
    kb.timer = mp.add_periodic_timer(KB_STEP, kb_step)
    kb_step()
end

-- finish removes the overlay and ends the transition. why is nil when the
-- transition ended normally. Ken Burns of the item on the screen starts here.
local function finish(why)
    if deadman then deadman:kill(); deadman = nil end
    if not S then return end
    if S.timer then S.timer:kill() end
    mp.commandv("overlay-remove", COVER)
    if S.kind == "split" then mp.commandv("overlay-remove", COVER2) end
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
    kb_begin()
end

-- show puts a part of a picture on the screen: w x h pixels from the byte
-- offset off, at x, y. id is the overlay; the default is COVER.
local function show(ptr, off, x, y, w, h, id)
    id = id or COVER
    if w < 1 or h < 1 then
        mp.commandv("overlay-remove", id)
        return true
    end
    return mp.commandv("overlay-add", id, x, y, addr(ptr), off, "bgra", w, h, S.stride)
end

-- dot shows a square of one colour with the alpha a (0 to 255), black or white,
-- scaled to the screen. It replaces the copy in the same overlay, so the change
-- is one step and there is never a frame with no cover. mpv copies the bitmap at
-- each call, so one buffer is enough.
--
-- The square has DOT x DOT pixels and not one pixel. vo=gpu puts a clear border
-- of one pixel around each bitmap and smooths the scaled bitmap into it. With one
-- pixel, the whole screen fades to half alpha toward its edge: the dip is dark in
-- the centre and the next item shows at the edges. A larger square keeps this to
-- a few pixels at the edge.
local DOT = 256
local pixel = ffi.new("int32_t[?]", DOT * DOT)
local function dot(a, white)
    local v = white and bor(lshift(a, 24), lshift(a, 16), lshift(a, 8), a) or lshift(a, 24)
    for i = 0, DOT * DOT - 1 do pixel[i] = v end
    return mp.commandv("overlay-add", COVER, 0, 0, addr(pixel), 0, "bgra", DOT, DOT, DOT * 4, S.w, S.h)
end

-- scale writes src * a / 256 into dst for all four channels of n premultiplied
-- pixels. orm is ORed into each pixel; OPAQUE keeps the result opaque. With
-- white, the pixels go to white and not to black: the result is the sum of
-- src * a / 256 and 255 * (256 - a) / 256.
local function scale(src8, dst, n, a, orm, white)
    local src = ffi.cast("const int32_t *", src8)
    local add = 0
    if white then add = (256 - a) * 255 * 65537 end
    for i = 0, n - 1 do
        local px = src[i]
        local rb = band(px, 0x00FF00FF) * a + add
        local ga = band(rshift(px, 8), 0x00FF00FF) * a + add
        dst[i] = bor(band(rshift(rb, 8), 0x00FF00FF), band(ga, -16711936), orm)
    end
end

-- pan moves B. The value is in pixels of the screen; video-pan takes a part of
-- the size of the video on the screen.
local function pan(x, y)
    mp.set_property_number("video-pan-x", x / S.vw)
    mp.set_property_number("video-pan-y", y / S.vh)
end

-- zoomout shows the copy at the part s (0 to 1) of its size, at the centre.
local function zoomout(s)
    local w, h = math.floor(S.w * s + 0.5), math.floor(S.h * s + 0.5)
    if w < 1 or h < 1 then
        mp.commandv("overlay-remove", COVER)
        return true
    end
    return mp.commandv("overlay-add", COVER, math.floor((S.w - w) / 2), math.floor((S.h - h) / 2),
        addr(S.base), 0, "bgra", S.w, S.h, S.stride, w, h)
end

-- doors shows the two halves of the copy, each one d pixels away from the
-- centre. The left half is in COVER and the right half is in COVER2. They do
-- not touch the same pixel, and with d = 0 they are the whole copy.
local function doors(d)
    local half = math.floor(S.w / 2)
    local ok = show(S.base, d * 4, 0, 0, half - d, S.h)
    return show(S.base, half * 4, half + d, 0, S.w - half - d, S.h, COVER2) and ok
end

-- step draws the transition at the time now.
local function step()
    local ok, err = pcall(function()
        local p = (mp.get_time() - S.t0) / (S.ms / 1000)
        if p >= 1 then finish() return end
        local W, H, R, k = S.w, S.h, S.stride, S.kind
        local dx, dy = math.floor(W * p), math.floor(H * p)
        local r
        if k == "fade" or k == "fade-white" then
            if p < 0.5 then
                scale(S.base, S.buf, W * H, math.floor((1 - 2 * p) * 256 + 0.5), OPAQUE, k == "fade-white")
                r = show(S.buf, 0, 0, 0, W, H)
            else
                r = dot(math.floor((1 - p) * 510 + 0.5), k == "fade-white")
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
        -- A slide-in moves B in over the copy, which stays where it is. B moves
        -- first, as in the push. The copy gets the crop of the wipe.
        elseif k == "slide-in-left" then
            pan(W - dx, 0)
            r = show(S.base, 0, 0, 0, W - dx, H)
        elseif k == "slide-in-right" then
            pan(dx - W, 0)
            r = show(S.base, dx * 4, dx, 0, W - dx, H)
        elseif k == "slide-in-up" then
            pan(0, H - dy)
            r = show(S.base, 0, 0, 0, W, H - dy)
        elseif k == "slide-in-down" then
            pan(0, dy - H)
            r = show(S.base, dy * R, 0, dy, W, H - dy)
        -- A slide-out moves the copy away and B stays where it is.
        elseif k == "slide-out-left" then r = show(S.base, dx * 4, 0, 0, W - dx, H)
        elseif k == "slide-out-right" then r = show(S.base, 0, dx, 0, W - dx, H)
        elseif k == "slide-out-up" then r = show(S.base, dy * R, 0, 0, W, H - dy)
        elseif k == "slide-out-down" then r = show(S.base, 0, 0, dy, W, H - dy)
        elseif k == "zoom-out" then r = zoomout(1 - p)
        elseif k == "split" then r = doors(math.floor(math.floor(W / 2) * p))
        end
        if not r then finish("the overlay failed") end
    end)
    if not ok then finish("error: " .. tostring(err)) end
end

-- start begins the movement when B shows its first frame.
local function start()
    if deadman then deadman:kill(); deadman = nil end
    if S.kind == "join" then
        -- B goes on from the frame that the moving crossfade showed last.
        finish()
        return
    end
    S.t0 = mp.get_time()
    local k = S.kind
    if k == "fade" or k == "fade-white" or k == "crossfade" then
        S.buf = ffi.new("int32_t[?]", S.w * S.h)
    end
    if k:sub(1, 5) == "push-" or k:sub(1, 9) == "slide-in-" then
        local d = mp.get_property_native("osd-dimensions")
        S.vw = math.max(1, d.w - d.ml - d.mr)
        S.vh = math.max(1, d.h - d.mt - d.mb)
        S.pan = true
    end
    S.timer = mp.add_periodic_timer(STEP, step)
    step()
end

-- drops gives the dropped frames of the file so far.
local function drops()
    return mp.get_property_number("frame-drop-count", 0) +
        mp.get_property_number("decoder-frame-drop-count", 0)
end

-- report puts the result of a moving crossfade into user-data/pptr/moved.
local function report(dropped, frames, failed)
    moved = moved + 1
    mp.set_property_native("user-data/pptr/moved",
        { count = moved, dropped = dropped, frames = frames, failed = failed })
end

-- video gives the video track of the file, or of the file that video-add
-- added last.
local function video(external)
    local found = nil
    for _, t in ipairs(mp.get_property_native("track-list", {})) do
        if t.type == "video" and (t.external == true) == external then
            if not external then return t end
            found = t
        end
    end
    return found
end

-- shape gives the width and height of a track, or nil for a picture that is
-- turned or has pixels that are not square: the filter graph would show it
-- in a different shape than mpv does.
local function shape(t)
    local w, h = t["demux-w"], t["demux-h"]
    if not w or not h or w < 2 or h < 2 then return nil end
    if (t["demux-rotation"] or 0) ~= 0 then return nil end
    local par = t["demux-par"]
    if par and math.abs(par - 1) > 0.01 then return nil end
    return w, h
end

-- span gives the end of the item in seconds: its duration, or its end option
-- when that is earlier. It gives nil when mpv knows no duration.
local function span()
    local length = mp.get_property_number("duration")
    local stop = tonumber(mp.get_property("end") or "")
    if stop and (not length or stop < length) then length = stop end
    return length
end

-- fit starts an item that continues a moving crossfade at 0 when it is too
-- short for that. The mix showed the first part of the item, and the item
-- starts after it. If the item is not longer than the mix, it would end at
-- once, and nothing of it would show after the mix.
local function fit()
    local begin = tonumber(mp.get_property("start") or "") or 0
    if begin <= 0 then return end
    local length = span()
    if length and length - begin < LEFT then
        mp.set_property("file-local-options/start", "0")
    end
end

-- prepare sets up a moving crossfade from this item into the next one. It runs
-- before mpv selects the tracks of the item. Each condition that it does not
-- find leaves the crossfade with the copy of the screen.
local function prepare()
    P = nil
    if broken or mp.get_opt("pptr-kind") ~= "crossfade" then return end
    if mp.get_property_native("user-data/pptr/motion") ~= "yes" then return end
    local d = (tonumber(mp.get_opt("pptr-ms") or "") or 0) / 1000
    local count = mp.get_property_number("playlist-count", 0)
    local pos = mp.get_property_number("playlist-pos", -1)
    if d <= 0 or count < 2 or pos < 0 then return end

    local a = video(false)
    if not a or a.image or a.albumart then return end
    local w, h = shape(a)
    local fps = a["demux-fps"]
    if not w or not fps or fps <= 0 then return end
    local length = span()
    -- An item that goes on from a moving crossfade starts later than 0.
    local begin = tonumber(mp.get_property("start") or "") or 0
    if not length or length - begin < d + LEAD then return end

    local next = (pos + 1) % count
    local path = mp.get_property("playlist/" .. next .. "/filename")
    local id = mp.get_property_number("playlist/" .. next .. "/id")
    if not path or not id or not mp.commandv("video-add", path, "auto") then return end
    local b = video(true)
    if not b then return end
    local bw, bh = shape(b)
    if not bw or math.abs(bw / bh - w / h) > 0.01 then
        -- The two pictures have a different shape. mpv shows each one in its
        -- own frame, and the mix would show B in the frame of A.
        mp.commandv("video-remove", b.id)
        return
    end

    -- B gets the size of A and an alpha channel that goes from clear to opaque
    -- in d seconds. setpts moves its first frame to the start of the mix.
    -- overlay passes the frames of A through until B has a frame, so A gets no
    -- work before the mix. (xfade takes 4:4:4 frames only: it would convert
    -- each frame of A.) An image gives one frame, and loop repeats it.
    local graph = string.format(
        "[vid%d]%sscale=%d:%d,setsar=1,format=yuva420p,fade=t=in:d=%.3f:alpha=1," ..
        "setpts=PTS-STARTPTS+%.3f/TB[mix];[vid%d][mix]overlay[vo]",
        b.id, b.image and string.format("loop=loop=-1:size=1,fps=%.6f,", fps) or "",
        w, h, d, length - d, a.id)
    mp.set_property("file-local-options/lavfi-complex", graph)
    mp.set_property("file-local-options/end", string.format("%.3f", length))
    -- The filters need the frames in memory. A decoder that keeps them in the
    -- GPU gives them to the graph in a form that it cannot read.
    local hwdec = mp.get_property("hwdec", "no")
    if hwdec ~= "no" and not hwdec:find("copy", 1, true) then
        mp.set_property("file-local-options/hwdec", "auto-copy")
    end
    P = { pos = pos, id = id, d = d, at = length - d, stop = length, image = b.image,
          frames = math.floor(d * fps + 0.5) }
    mp.msg.verbose("moving crossfade: " .. graph)
end

-- settle ends the moving crossfade of the item that ends. It gives "join" when
-- the mix reached its end, so B goes on from there. Else end-file decides if
-- the item stopped because of a fault.
local function settle()
    local p = P
    P = nil
    if not p then return nil end
    if p.timer then p.timer:kill() end
    p.now = mp.get_property_number("time-pos", 0)
    mp.msg.verbose(string.format("moving crossfade: unload at %.3f of %.3f", p.now, p.stop))
    if p.shown and p.now >= p.stop - LATE then
        report(drops() - (p.base or drops()), p.frames, false)
        J = { id = p.id, at = (not p.image) and p.d or nil }
        return "join"
    end
    F = p
    return nil
end

-- reshape gives the copy of the screen in the shape of the picture, or nil
-- when the copy is right already. vo=drm has no screenshot of its own: mpv
-- scales the video frame to the size of the window and does not keep its
-- aspect (player/screenshot.c). On a 16:10 screen a 16:9 picture then fills
-- the bars, and the shape jumps when the copy shows. The function scales the
-- copy back into the video rectangle (osd-dimensions) and makes the bars
-- opaque black. vo=gpu gives a copy of the window that is right.
local function reshape(src8, W, H)
    if mp.get_property("current-vo") ~= "drm" then return nil end
    local d = mp.get_property_native("osd-dimensions")
    -- A margin below 0 means that the picture is moved or zoomed. A push that
    -- ended a moment ago does this: mpv has not drawn the picture at its place
    -- yet. Such margins are not the video rectangle, and the loops below write
    -- with them. The copy then stays as it is.
    if d.ml < 0 or d.mr < 0 or d.mt < 0 or d.mb < 0 then return nil end
    local x0, y0 = d.ml, d.mt
    local vw, vh = W - d.ml - d.mr, H - d.mt - d.mb
    if vw < 1 or vh < 1 or (vw == W and vh == H) then return nil end
    local src = ffi.cast("const int32_t *", src8)
    local dst = ffi.new("int32_t[?]", W * H)
    for i = 0, W * H - 1 do dst[i] = OPAQUE end
    local map = ffi.new("int32_t[?]", vw)
    for x = 0, vw - 1 do map[x] = math.floor(x * W / vw) end
    for y = 0, vh - 1 do
        local row = src + math.floor(y * H / vh) * W
        local out = dst + (y0 + y) * W + x0
        if vw == W then
            ffi.copy(out, row, W * 4)
        else
            for x = 0, vw - 1 do out[x] = bor(row[map[x]], OPAQUE) end
        end
    end
    return dst
end

-- cover puts the copy of the screen on top before mpv unloads item A.
local function cover()
    kb_stop()
    local kind = settle() or mp.get_opt("pptr-kind") or "cut"
    if S then
        if not S.t0 then
            -- This item showed no frame: it did not load. The copy still shows
            -- the last frame that did show, so it stays for the next item.
            return
        end
        finish() -- the item was shorter than its transition
    end
    local ms = tonumber(mp.get_opt("pptr-ms") or "") or 0
    if kind == "cut" or ms <= 0 then
        -- A cut after Ken Burns still needs the copy. B would show its first
        -- frame with the zoom of A, and the zoom goes back to 0 under the copy.
        if not kbview then return end
        kind = "join"
    end
    if kind ~= "join" and not KINDS[kind] then
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
    local shaped = reshape(S.base, S.w, S.h)
    if shaped then
        S.own = shaped
        S.base = ffi.cast("const uint8_t *", shaped)
    end
    local shown
    if kind == "split" then shown = doors(0) else shown = show(S.base, 0, 0, 0, S.w, S.h) end
    if not shown then
        finish("the overlay failed")
        return
    end
    deadman = mp.add_timeout(DEADMAN, function()
        finish("the next item did not show in " .. DEADMAN .. " s")
    end)
end

-- The next item of a moving crossfade starts where the mix stopped, so no
-- part of it plays two times.
mp.add_hook("on_load", 50, function()
    local j = J
    J = nil
    if not j or not j.at then return end
    local pos = mp.get_property_number("playlist-pos", -1)
    if mp.get_property_number("playlist/" .. pos .. "/id") == j.id then
        mp.set_property("file-local-options/start", string.format("%.3f", j.at))
    end
end)

mp.add_hook("on_preloaded", 50, function()
    local ok, err = pcall(function() fit() prepare() end)
    if not ok then
        P = nil
        fault("moving crossfade: error: " .. tostring(err))
    end
end)

mp.add_hook("on_unload", 50, function()
    local ok, err = pcall(cover)
    -- The copy is on top now, or the item ends with a cut. The next item must
    -- not start with the zoom and the pan of this one.
    pcall(kb_view_reset)
    if ok then return end
    if S then
        finish("error: " .. tostring(err))
    else
        fault("error: " .. tostring(err) .. "; the transition is a cut")
    end
end)

-- The filter graph writes each of its faults with the prefix "lavfi" (mpv
-- filters/f_lavfi.c). That is the sign that an item ended because of the graph,
-- and not because of the file: a file can also end early, or fail to decode.
mp.enable_messages("error")
mp.register_event("log-message", function(e)
    local p = P or F
    if p and e.prefix == "lavfi" then p.lavfi = true end
end)

mp.register_event("end-file", function(e)
    local f = F
    F = nil
    if not f or not f.lavfi or (e.reason ~= "eof" and e.reason ~= "error") then return end
    -- The item ended on its own before the end of its mix, and the filter graph
    -- wrote a fault. A graph that fails in its setup makes mpv refuse the item.
    broken = true
    report(0, 0, true)
    fault(string.format("crossfade: the moving crossfade failed at %.1f s of %.1f s; " ..
        "the player makes no more moving crossfades", f.now, f.stop))
    if not f.shown then
        -- The item showed nothing. Play it again, now with no filter graph.
        mp.commandv("playlist-play-index", f.pos)
    end
end)

-- kb_arm is called at the first picture of an image that has pptr-kb. Ken
-- Burns waits for the end of the transition into the image.
local function kb_arm()
    if kbseen then return end
    kbseen = true
    local total = tonumber(mp.get_opt("pptr-kb") or "")
    if not total or total < KB_MIN then return end
    local v = video(false)
    if not v or not v.image then return end
    kbwait = { t0 = mp.get_time(), total = total }
    if not S then kb_begin() end
end

mp.register_event("start-file", function() kbseen = false end)

mp.register_event("playback-restart", function()
    if S and not S.t0 then
        local ok, err = pcall(start)
        if not ok then finish("error: " .. tostring(err)) end
    end
    local ok, err = pcall(kb_arm)
    if not ok then fault("ken burns: error: " .. tostring(err)) end
    if P and not P.shown then
        -- A shows its first frame. Count the dropped frames of the mix only.
        P.shown = true
        local wait = P.at - mp.get_property_number("time-pos", 0)
        P.timer = mp.add_timeout(math.max(0, wait), function()
            if P then P.base = drops() end
        end)
    end
end)
