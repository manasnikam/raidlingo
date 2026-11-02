from pathlib import Path
import json
import re
import subprocess
import textwrap

root = Path(__file__).resolve().parent
segments = json.loads((root / 'assets/narration.json').read_text())
captions = []

def stamp(seconds):
    ms = round(seconds * 1000)
    hours, ms = divmod(ms, 3600000)
    minutes, ms = divmod(ms, 60000)
    seconds, ms = divmod(ms, 1000)
    return f'{hours:02}:{minutes:02}:{seconds:02},{ms:03}'

command = ['ffmpeg', '-hide_banner', '-loglevel', 'error', '-y']
filters = []
for index, segment in enumerate(segments):
    path = root / f'assets/narration-{index}.aiff'
    command += ['-i', str(path)]
    duration = float(subprocess.check_output([
        'ffprobe', '-v', 'error', '-show_entries', 'format=duration',
        '-of', 'default=nw=1:nk=1', str(path)
    ]))
    sentences = re.split(r'(?<=[.!?])\s+', segment['text'])
    weights = [len(sentence.split()) for sentence in sentences]
    total = sum(weights)
    offset = 0
    for sentence, weight in zip(sentences, weights):
        start = segment['start'] + duration * offset / total
        offset += weight
        end = segment['start'] + duration * offset / total
        captions.append(
            f'{len(captions) + 1}\n{stamp(start)} --> {stamp(end)}\n'
            + textwrap.fill(sentence, 72) + '\n'
        )
    filters.append(
        f'[{index}:a]aresample=48000,adelay={segment["start"] * 1000}:all=1[a{index}]'
    )

inputs = ''.join(f'[a{i}]' for i in range(len(segments)))
filters.append(
    inputs + f'amix=inputs={len(segments)}:normalize=0,'
    'apad=whole_dur=155,atrim=duration=155,loudnorm=I=-16:TP=-1.5:LRA=11[out]'
)
command += ['-filter_complex', ';'.join(filters), '-map', '[out]',
            '-ar', '48000', '-c:a', 'pcm_s16le', str(root / 'assets/narration.wav')]
subprocess.run(command, check=True)
(root / 'raidlingo-demo.srt').write_text('\n'.join(captions))
print('Created narration.wav and raidlingo-demo.srt')
