#!/usr/bin/env python3
"""Build bounded, source-addressable inputs from this fixture; never call a model."""
from __future__ import annotations
import argparse
import hashlib
import json
from pathlib import Path
import posixpath
import sys
import zipfile
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parent
MAX_FILE = 16 * 1024 * 1024
MAX_XML = 2 * 1024 * 1024
MAX_TEXT = 180_000
W = '{http://schemas.openxmlformats.org/wordprocessingml/2006/main}'
A = '{http://schemas.openxmlformats.org/drawingml/2006/main}'
P = '{http://schemas.openxmlformats.org/presentationml/2006/main}'
R = '{http://schemas.openxmlformats.org/officeDocument/2006/relationships}'

class FixtureError(ValueError):
    pass

def local_path(value: str) -> Path:
    p = (ROOT / value).resolve()
    if not p.is_relative_to(ROOT) or p == ROOT:
        raise FixtureError('source path leaves fixture root')
    return p

def xml(z: zipfile.ZipFile, name: str) -> ET.Element:
    info = z.getinfo(name)
    if info.file_size > MAX_XML:
        raise FixtureError('XML entry exceeds size limit')
    raw = z.read(name)
    if b'<!DOCTYPE' in raw.upper() or b'<!ENTITY' in raw.upper():
        raise FixtureError('DTD/entity declarations are not accepted')
    return ET.fromstring(raw)

def extract(path: Path, kind: str) -> tuple[list[dict], list[str]]:
    if path.stat().st_size > MAX_FILE:
        raise FixtureError('source exceeds size limit')
    if kind == 'markdown':
        text = path.read_text(encoding='utf-8')
        return ([{'locator': f'line:{i}', 'text': t} for i, t in enumerate(text.splitlines(), 1) if t.strip()], [])
    if kind not in ('docx', 'pptx'):
        raise FixtureError('unsupported reader kind')
    segments = []
    warnings = []
    with zipfile.ZipFile(path) as z:
        infos = z.infolist()
        if len(infos) > 512 or sum(i.file_size for i in infos) > MAX_FILE:
            raise FixtureError('Office archive exceeds entry/expanded-size limits')
        if len({i.filename for i in infos}) != len(infos):
            raise FixtureError('duplicate archive entry')
        for i in infos:
            if i.filename.startswith('/') or '\\' in i.filename or '..' in i.filename.split('/'):
                raise FixtureError('unsafe archive entry name')
        if any('/media/' in i.filename for i in infos):
            warnings.append('embedded images are not OCRed; check them manually')
        if kind == 'docx':
            root = xml(z, 'word/document.xml')
            for i, para in enumerate(root.iter(W + 'p'), 1):
                text = ''.join(n.text or '' for n in para.iter(W + 't'))
                if text.strip():
                    segments.append({'locator': f'word/document.xml:paragraph:{i}', 'text': text})
            warnings.append('DOCX locators identify XML paragraphs, not rendered page numbers; headers, footers, comments and drawing semantics are not analyzed')
        else:
            root = xml(z, 'ppt/presentation.xml')
            relroot = xml(z, 'ppt/_rels/presentation.xml.rels')
            rels = {n.attrib['Id']: n.attrib for n in relroot}
            for i, sid in enumerate(root.iter(P + 'sldId'), 1):
                rel = rels[sid.attrib[R + 'id']]
                if rel.get('TargetMode') == 'External':
                    raise FixtureError('external slide relationship')
                target_ref = rel['Target']
                if target_ref.startswith('//') or '\\' in target_ref:
                    raise FixtureError('invalid slide relationship URI')
                # OPC permits a package-absolute /ppt/... target; it is not a host path.
                target = posixpath.normpath(target_ref[1:] if target_ref.startswith('/') else posixpath.join('ppt', target_ref))
                if not target.startswith('ppt/slides/') or not target.endswith('.xml'):
                    raise FixtureError('slide relationship leaves slide directory')
                slide = xml(z, target)
                for j, para in enumerate(slide.iter(A + 'p'), 1):
                    text = ''.join(n.text or '' for n in para.iter(A + 't'))
                    if text.strip():
                        segments.append({'locator': f'slide:{i}:paragraph:{j}', 'text': text})
            warnings.append('PPTX extraction covers slide text only; speaker notes, chart data and SmartArt require separate analysis')
    if not segments:
        raise FixtureError('no extractable text; do not treat an empty extraction as analyzed')
    return segments, warnings

def assemble(pack: dict, stage: str) -> dict:
    if stage not in pack['stages']:
        raise FixtureError('unknown stage')
    sources = {s['id']: s for s in pack['sources']}
    if len(sources) != len(pack['sources']):
        raise FixtureError('duplicate source ID')
    result = {'fixture_id': pack['id'], 'stage': stage,
              'purpose': 'fixture input only; not evidence of an application or model acceptance run',
              'sources': [], 'gaps': [], 'instruction': pack.get('analysis_instruction', '')}
    total = 0
    for source_id in pack['stages'][stage]['source_ids']:
        source = sources[source_id]
        record = {k: source[k] for k in ('id', 'kind', 'authority', 'label')}
        record['source_class'] = source.get('source_class', 'unverified')
        if source.get('availability') == 'unavailable':
            result['gaps'].append({'source_id': source_id, 'reason': source['reason']})
            continue
        try:
            path = local_path(source['path'])
            if path.stat().st_size > MAX_FILE:
                raise FixtureError('source exceeds size limit')
            actual = hashlib.sha256(path.read_bytes()).hexdigest()
            if actual != source['sha256']:
                raise FixtureError('source hash changed; refresh and review the fixture baseline explicitly')
            segments, warnings = extract(path, source['reader'])
            count = sum(len(x['text']) for x in segments)
            if total + count > MAX_TEXT:
                raise FixtureError('text budget exceeded; source was not silently truncated')
            total += count
            record.update(sha256=actual, segments=segments, warnings=warnings)
            result['sources'].append(record)
        except (FixtureError, OSError, KeyError, zipfile.BadZipFile, ET.ParseError, UnicodeError) as e:
            result['gaps'].append({'source_id': source_id, 'reason': str(e)})
    return result

def verify(pack: dict) -> dict:
    ids = {s['id'] for s in pack['sources']}
    if len(ids) != len(pack['sources']):
        raise FixtureError('duplicate source ID')
    all_sources = set()
    for name, stage in pack['stages'].items():
        if not set(stage['source_ids']) <= ids:
            raise FixtureError(f'unknown source in stage {name}')
        result = assemble(pack, name)
        found = {g['source_id'] for g in result['gaps']}
        if found != set(stage.get('expected_gap_source_ids', [])):
            raise FixtureError(f'unexpected gaps for {name}: {result["gaps"]}')
        for record in result['sources']:
            required = next(s for s in pack['sources'] if s['id'] == record['id']).get('required_text', [])
            extracted = '\n'.join(x['text'] for x in record['segments'])
            if any(t not in extracted for t in required):
                raise FixtureError(f"required text absent from actual attachment: {record['id']}")
        all_sources.update(stage['source_ids'])
    if all_sources != ids:
        raise FixtureError('manifest has sources not exercised by any stage')
    for case in pack['cases']:
        if case['stage'] not in pack['stages'] or not case['must_observe']:
            raise FixtureError('case lacks a valid stage or assertions')
    return {'status': 'passed', 'scope': 'fixture integrity and actual attachment text extraction only',
            'stages': len(pack['stages']), 'sources': len(ids), 'cases': len(pack['cases']),
            'application_acceptance': 'not_run', 'semantic_model_evaluation': 'not_run'}

def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--stage', default='initial')
    parser.add_argument('--check', action='store_true')
    parser.add_argument('--output', type=Path)
    args = parser.parse_args()
    pack = json.loads((ROOT / 'pack.json').read_text())
    try:
        result = verify(pack) if args.check else assemble(pack, args.stage)
    except (FixtureError, KeyError) as e:
        print(json.dumps({'status': 'failed', 'error': str(e)}, ensure_ascii=False), file=sys.stderr)
        return 1
    content = json.dumps(result, ensure_ascii=False, indent=2) + '\n'
    if args.output:
        args.output.write_text(content)
    else:
        print(content, end='')
    return 0

if __name__ == '__main__':
    raise SystemExit(main())
