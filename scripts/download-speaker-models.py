"""Download public sherpa-onnx speaker models; no account or API token needed."""
from pathlib import Path
import tarfile
import urllib.request


def download(url, path):
    if not path.exists():
        temporary = path.with_suffix(path.suffix + '.partial')
        print('Downloading', path.name, flush=True)
        urllib.request.urlretrieve(url, temporary)
        temporary.replace(path)


def main():
    root = Path(__file__).resolve().parent.parent / 'models' / 'diarization'
    root.mkdir(parents=True, exist_ok=True)
    archive = root / 'segmentation.tar.bz2'
    model = root / 'sherpa-onnx-pyannote-segmentation-3-0' / 'model.onnx'
    if not model.exists():
        download('https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-segmentation-models/sherpa-onnx-pyannote-segmentation-3-0.tar.bz2', archive)
        with tarfile.open(archive) as source:
            source.extractall(root, filter='data')
    download('https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-recongition-models/nemo_en_titanet_small.onnx', root / 'nemo_en_titanet_small.onnx')
    print('Speaker models are ready.', flush=True)


if __name__ == '__main__':
    main()
