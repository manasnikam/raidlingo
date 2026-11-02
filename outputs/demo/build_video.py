from pathlib import Path
import json
import subprocess

root = Path(__file__).resolve().parent

def presentation_start(path):
    raw = subprocess.check_output([
        'ffmpeg', '-hide_banner', '-loglevel', 'error', '-i', str(path),
        '-t', '15', '-vf', 'format=rgb24,crop=2:2:1120:620,fps=10',
        '-pix_fmt', 'rgb24', '-f', 'rawvideo', 'pipe:1'
    ])
    saw_button = False
    for index in range(len(raw) // 12):
        pixels = raw[index * 12:(index + 1) * 12]
        color = [sum(pixels[channel::3]) / 4 for channel in range(3)]
        orange = color[0] - color[1] > 20 and color[2] < 210
        if orange:
            saw_button = True
        elif saw_button:
            return index / 10
    raise RuntimeError(f'Could not locate the presentation start in {path.name}')

first = root / 'raw/first-direction.mov'
tail = root / 'raw/tail.mov'
first_start = presentation_start(first)
tail_start = presentation_start(tail)
edit = {
    'duration_seconds': 155,
    'resolution': '1280x720',
    'first_take_start': first_start,
    'first_take_duration': 65,
    'tail_take_start': tail_start,
    'tail_take_duration': 90,
    'reverse_example': 'Earlier verified live Spanish test, clearly labeled in the video',
    'narration': 'Local macOS Samantha synthetic voice',
}
(root / 'edit.json').write_text(json.dumps(edit, indent=2))
filters = (
    f'[0:v]trim=start={first_start}:duration=65,setpts=PTS-STARTPTS,fps=30[a];'
    f'[1:v]trim=start={tail_start}:duration=90,setpts=PTS-STARTPTS,fps=30[b];'
    '[a][b]concat=n=2:v=1:a=0,'
    'drawbox=x=0:y=716:w=1280:h=4:color=0xeeeade:t=fill,'
    'fade=t=in:st=0:d=0.35,fade=t=out:st=154.5:d=0.5[v]'
)
command = [
    'ffmpeg', '-hide_banner', '-loglevel', 'error', '-y',
    '-i', str(first), '-i', str(tail), '-i', str(root / 'assets/narration.wav'),
    '-i', str(root / 'raidlingo-demo.srt'), '-filter_complex', filters,
    '-map', '[v]', '-map', '2:a', '-map', '3:s', '-map_metadata', '-1',
    '-c:v', 'libx264', '-preset', 'medium', '-crf', '18', '-threads', '4',
    '-pix_fmt', 'yuv420p', '-c:a', 'aac', '-b:a', '160k', '-c:s', 'mov_text',
    '-metadata', 'title=Raidlingo — Two channels. One conversation.',
    '-metadata:s:s:0', 'language=eng', '-metadata:s:s:0', 'title=English captions',
    '-disposition:s:0', 'default', '-movflags', '+faststart', '-t', '155',
    str(root / 'raidlingo-demo.mp4')
]
print(json.dumps(edit), flush=True)
subprocess.run(command, check=True)
print('Created raidlingo-demo.mp4')
