"""Local voice clustering. Receives only a temporary 16 kHz mono PCM WAV.

stdout is JSON timing data; no speech or transcript leaves this computer.
See https://k2-fsa.github.io/sherpa/onnx/speaker-diarization/python.html
"""
import argparse
import json
import wave

import numpy as np
import sherpa_onnx


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--audio', required=True)
    parser.add_argument('--segmentation', required=True)
    parser.add_argument('--embedding', required=True)
    parser.add_argument('--threshold', type=float, default=0.9)
    args = parser.parse_args()
    with wave.open(args.audio, 'rb') as source:
        if (source.getframerate(), source.getnchannels(), source.getsampwidth()) != (16000, 1, 2):
            raise ValueError('Expected 16 kHz mono PCM16 WAV')
        samples = np.frombuffer(source.readframes(source.getnframes()), dtype='<i2').astype(np.float32) / 32768.0

    config = sherpa_onnx.OfflineSpeakerDiarizationConfig(
        segmentation=sherpa_onnx.OfflineSpeakerSegmentationModelConfig(
            pyannote=sherpa_onnx.OfflineSpeakerSegmentationPyannoteModelConfig(model=args.segmentation),
            num_threads=2,
        ),
        embedding=sherpa_onnx.SpeakerEmbeddingExtractorConfig(model=args.embedding, num_threads=2),
        clustering=sherpa_onnx.FastClusteringConfig(num_clusters=-1, threshold=args.threshold),
        min_duration_on=0.3,
        min_duration_off=0.5,
    )
    if not config.validate():
        raise ValueError('Speaker models are unavailable or invalid')
    diarizer = sherpa_onnx.OfflineSpeakerDiarization(config)
    result = diarizer.process(samples).sort_by_start_time()
    print(json.dumps([{'start': float(turn.start), 'end': float(turn.end), 'speaker': int(turn.speaker)} for turn in result]))


if __name__ == '__main__':
    main()
