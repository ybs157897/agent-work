/**
 * IntelliJ ExpUI tree icons (from intellij-community platform/icons/src/expui).
 * Light variants only — Apache 2.0.
 */
import folderUrl from '../assets/idea-icons/nodes/folder.svg?url'
import resourcesRootUrl from '../assets/idea-icons/nodes/resourcesRoot.svg?url'
import testResourcesRootUrl from '../assets/idea-icons/nodes/testResourcesRoot.svg?url'
import sourceRootUrl from '../assets/idea-icons/nodes/sourceRoot.svg?url'
import testRootUrl from '../assets/idea-icons/nodes/testRoot.svg?url'
import testSourceFolderUrl from '../assets/idea-icons/nodes/testSourceFolder.svg?url'
import packageUrl from '../assets/idea-icons/nodes/package.svg?url'
import webFolderUrl from '../assets/idea-icons/nodes/webFolder.svg?url'
import moduleUrl from '../assets/idea-icons/nodes/module.svg?url'
import moduleJavaUrl from '../assets/idea-icons/nodes/moduleJava.svg?url'
import ppJdkUrl from '../assets/idea-icons/nodes/ppJdk.svg?url'
import ppLibUrl from '../assets/idea-icons/nodes/ppLib.svg?url'
import mavenPomUrl from '../assets/idea-icons/fileTypes/mavenPom.svg?url'
import javaDocFolderUrl from '../assets/idea-icons/nodes/javaDocFolder.svg?url'
import libraryFolderUrl from '../assets/idea-icons/nodes/libraryFolder.svg?url'
import generatedSourceUrl from '../assets/idea-icons/nodes/generatedSource.svg?url'
import excludeRootUrl from '../assets/idea-icons/nodes/excludeRoot.svg?url'
import ppWebUrl from '../assets/idea-icons/nodes/ppWeb.svg?url'

import javaUrl from '../assets/idea-icons/fileTypes/java.svg?url'
import javaClassUrl from '../assets/idea-icons/fileTypes/javaClass.svg?url'
import xmlUrl from '../assets/idea-icons/fileTypes/xml.svg?url'
import xhtmlUrl from '../assets/idea-icons/fileTypes/xhtml.svg?url'
import propertiesUrl from '../assets/idea-icons/fileTypes/properties.svg?url'
import jsonUrl from '../assets/idea-icons/fileTypes/json.svg?url'
import yamlUrl from '../assets/idea-icons/fileTypes/yaml.svg?url'
import htmlUrl from '../assets/idea-icons/fileTypes/html.svg?url'
import cssUrl from '../assets/idea-icons/fileTypes/css.svg?url'
import textUrl from '../assets/idea-icons/fileTypes/text.svg?url'
import imageUrl from '../assets/idea-icons/fileTypes/image.svg?url'
import archiveUrl from '../assets/idea-icons/fileTypes/archive.svg?url'
import sqlUrl from '../assets/idea-icons/fileTypes/sql.svg?url'
import gradleUrl from '../assets/idea-icons/fileTypes/gradle.svg?url'
import groovyUrl from '../assets/idea-icons/fileTypes/groovy.svg?url'
import javaScriptUrl from '../assets/idea-icons/fileTypes/javaScript.svg?url'
import gitignoreUrl from '../assets/idea-icons/fileTypes/gitignore.svg?url'
import configUrl from '../assets/idea-icons/fileTypes/config.svg?url'
import dockerUrl from '../assets/idea-icons/fileTypes/docker.svg?url'
import csvUrl from '../assets/idea-icons/fileTypes/csv.svg?url'
import fontUrl from '../assets/idea-icons/fileTypes/font.svg?url'
import anyTypeUrl from '../assets/idea-icons/fileTypes/anyType.svg?url'
import editorConfigUrl from '../assets/idea-icons/fileTypes/editorConfig.svg?url'
import httpUrl from '../assets/idea-icons/fileTypes/http.svg?url'
import i18nUrl from '../assets/idea-icons/fileTypes/i18n.svg?url'
import markdownUrl from '../assets/idea-icons/fileTypes/markdown.svg?url'
import shellUrl from '../assets/idea-icons/fileTypes/shell.svg?url'
import vueUrl from '../assets/idea-icons/fileTypes/vue.svg?url'
import tomlUrl from '../assets/idea-icons/fileTypes/toml.svg?url'

export type IconKind =
  | 'folder'
  | 'folderOpen'
  | 'resourcesRoot'
  | 'testResourcesRoot'
  | 'sourceRoot'
  | 'testRoot'
  | 'testSourceFolder'
  | 'package'
  | 'webFolder'
  | 'module'
  | 'moduleJava'
  | 'javaDocFolder'
  | 'libraryFolder'
  | 'generatedSource'
  | 'excludeRoot'
  | 'ppWeb'
  | 'java'
  | 'javaClass'
  | 'xml'
  | 'xhtml'
  | 'properties'
  | 'json'
  | 'yaml'
  | 'html'
  | 'css'
  | 'text'
  | 'image'
  | 'archive'
  | 'sql'
  | 'gradle'
  | 'groovy'
  | 'javaScript'
  | 'gitignore'
  | 'config'
  | 'docker'
  | 'csv'
  | 'font'
  | 'anyType'
  | 'editorConfig'
  | 'http'
  | 'i18n'
  | 'markdown'
  | 'shell'
  | 'vue'
  | 'toml'
  | 'file'
  | 'mavenPom'
  | 'jdk'
  | 'libraries'

const ICON_URL: Record<IconKind, string> = {
  folder: folderUrl,
  folderOpen: folderUrl,
  resourcesRoot: resourcesRootUrl,
  testResourcesRoot: testResourcesRootUrl,
  sourceRoot: sourceRootUrl,
  testRoot: testRootUrl,
  testSourceFolder: testSourceFolderUrl,
  package: packageUrl,
  webFolder: webFolderUrl,
  module: moduleUrl,
  moduleJava: moduleJavaUrl,
  javaDocFolder: javaDocFolderUrl,
  libraryFolder: libraryFolderUrl,
  generatedSource: generatedSourceUrl,
  excludeRoot: excludeRootUrl,
  ppWeb: ppWebUrl,
  java: javaUrl,
  javaClass: javaClassUrl,
  xml: xmlUrl,
  xhtml: xhtmlUrl,
  properties: propertiesUrl,
  json: jsonUrl,
  yaml: yamlUrl,
  html: htmlUrl,
  css: cssUrl,
  text: textUrl,
  image: imageUrl,
  archive: archiveUrl,
  sql: sqlUrl,
  gradle: gradleUrl,
  groovy: groovyUrl,
  javaScript: javaScriptUrl,
  gitignore: gitignoreUrl,
  config: configUrl,
  docker: dockerUrl,
  csv: csvUrl,
  font: fontUrl,
  anyType: anyTypeUrl,
  editorConfig: editorConfigUrl,
  http: httpUrl,
  i18n: i18nUrl,
  markdown: markdownUrl,
  shell: shellUrl,
  vue: vueUrl,
  toml: tomlUrl,
  file: textUrl,
  mavenPom: mavenPomUrl,
  jdk: ppJdkUrl,
  libraries: ppLibUrl,
}

const JAVA_ID = /^[A-Za-z_][A-Za-z0-9_]*$/

function extOf(name: string): string {
  const i = name.lastIndexOf('.')
  if (i <= 0) return ''
  return name.slice(i + 1).toLowerCase()
}

function baseName(name: string): string {
  return name.toLowerCase()
}

/** Heuristic: directory looks like a Java package segment under a source root. */
function isPackageDir(name: string, path: string): boolean {
  if (!JAVA_ID.test(name)) return false
  if (name === 'java' || name === 'kotlin' || name === 'scala' || name === 'resources') return false
  const p = path.replace(/\\/g, '/').toLowerCase()
  return (
    /\/(main|test)\/(java|kotlin|scala)(\/|$)/.test(`/${p}/`) ||
    /\/src\/(java|kotlin|scala)(\/|$)/.test(`/${p}/`) ||
    /\/(java|kotlin|scala)\/[a-z]/.test(`/${p}`)
  )
}

function classifyFolder(name: string, path: string, open?: boolean, modulePaths?: Set<string>): IconKind {
  if (modulePaths?.has(path)) return 'moduleJava'

  const n = baseName(name)
  const p = path.replace(/\\/g, '/').toLowerCase()

  // Exact / well-known resource & root folders (IDEA Project view)
  if (n === 'resources' || p.endsWith('/main/resources') || p.endsWith('/resources')) {
    if (p.includes('/test/') || p.includes('/testresources') || n === 'test-resources') {
      return 'testResourcesRoot'
    }
    return 'resourcesRoot'
  }
  if (
    n === 'test-resources' ||
    n === 'testresources' ||
    p.endsWith('/test/resources') ||
    p.endsWith('/testresources')
  ) {
    return 'testResourcesRoot'
  }
  if (n === 'java' && (p.includes('/main/java') || p.endsWith('/main/java') || /\/src\/java$/.test(p))) {
    return 'sourceRoot'
  }
  if (n === 'java' && (p.includes('/test/java') || p.endsWith('/test/java'))) {
    return 'testRoot'
  }
  if (n === 'kotlin' && p.includes('/main/')) return 'sourceRoot'
  if (n === 'kotlin' && p.includes('/test/')) return 'testRoot'
  if (n === 'test' && (p.endsWith('/src/test') || p.endsWith('/test'))) return 'testSourceFolder'
  if (n === 'webapp' || n === 'public' || n === 'static' || n === 'web' || n === 'www') return 'webFolder'
  if (n === 'templates' || n === 'static' || n === 'public') return 'ppWeb'
  if (n === 'lib' || n === 'libs' || n === 'library' || n === 'libraries') return 'libraryFolder'
  if (n === 'javadoc' || n === 'docs' || n === 'documentation') return 'javaDocFolder'
  if (n === 'generated' || n === 'generated-sources' || n === 'generated-test-sources') {
    return 'generatedSource'
  }
  if (
    n === 'target' ||
    n === 'build' ||
    n === 'out' ||
    n === 'node_modules' ||
    n === '.gradle' ||
    n === '.idea' ||
    n === 'dist'
  ) {
    return 'excludeRoot'
  }
  if (n === 'meta-inf') return 'config'
  if (n.endsWith('.iml') || n.endsWith('.ipr')) return 'module'
  if (isPackageDir(name, path)) return 'package'

  // open state still uses same ExpUI folder glyph (IDEA does not swap open artwork in ExpUI tree)
  return open ? 'folderOpen' : 'folder'
}

function classifyFile(name: string): IconKind {
  const n = baseName(name)
  const ext = extOf(name)

  // Special filenames
  if (n === 'dockerfile' || n.startsWith('dockerfile.') || n === 'docker-compose.yml' || n === 'docker-compose.yaml') {
    return 'docker'
  }
  if (n === 'pom.xml') return 'mavenPom'
  if (n === 'build.gradle' || n === 'build.gradle.kts' || n === 'settings.gradle' || n === 'settings.gradle.kts') {
    return 'gradle'
  }
  if (n === '.gitignore' || n === '.gitattributes' || n === '.gitmodules') return 'gitignore'
  if (n === '.editorconfig') return 'editorConfig'
  if (n === 'package.json' || n === 'tsconfig.json' || n === 'jsconfig.json') return 'json'
  if (n.startsWith('application.') && (ext === 'yml' || ext === 'yaml' || ext === 'properties')) {
    return ext === 'properties' ? 'properties' : 'yaml'
  }
  if (n === 'messages.properties' || n.startsWith('messages_') || n.includes('i18n')) return 'i18n'

  switch (ext) {
    case 'java':
      return 'java'
    case 'class':
      return 'javaClass'
    case 'xml':
    case 'xsd':
    case 'xsl':
    case 'xslt':
    case 'tld':
    case 'wsdl':
      return 'xml'
    case 'xhtml':
      return 'xhtml'
    case 'properties':
      return 'properties'
    case 'json':
      return 'json'
    case 'yml':
    case 'yaml':
      return 'yaml'
    case 'html':
    case 'htm':
      return 'html'
    case 'css':
      return 'css'
    case 'js':
    case 'mjs':
    case 'cjs':
    case 'jsx':
    case 'ts':
    case 'tsx':
      return 'javaScript'
    case 'vue':
      return 'vue'
    case 'md':
    case 'markdown':
    case 'mdx':
      return 'markdown'
    case 'sql':
      return 'sql'
    case 'gradle':
    case 'kts':
      return 'gradle'
    case 'groovy':
      return 'groovy'
    case 'sh':
    case 'bash':
    case 'zsh':
    case 'bat':
    case 'cmd':
    case 'ps1':
      return 'shell'
    case 'toml':
      return 'toml'
    case 'png':
    case 'jpg':
    case 'jpeg':
    case 'gif':
    case 'svg':
    case 'ico':
    case 'webp':
    case 'bmp':
      return 'image'
    case 'zip':
    case 'jar':
    case 'war':
    case 'ear':
    case 'tar':
    case 'gz':
    case 'tgz':
    case '7z':
    case 'rar':
      return 'archive'
    case 'ttf':
    case 'otf':
    case 'woff':
    case 'woff2':
    case 'eot':
      return 'font'
    case 'csv':
    case 'tsv':
      return 'csv'
    case 'http':
    case 'rest':
      return 'http'
    case 'txt':
    case 'log':
    case 'md5':
    case 'sha1':
      return 'text'
    case 'conf':
    case 'cfg':
    case 'ini':
    case 'env':
      return 'config'
    case 'iml':
    case 'ipr':
    case 'iws':
      return 'module'
    default:
      if (!ext) return n.startsWith('.') ? 'config' : 'anyType'
      return 'anyType'
  }
}

export type ClassifyOptions = {
  open?: boolean
  /** Workspace-relative path (for package / resources heuristics). */
  path?: string
  /** Maven module roots (workspace-relative). */
  modulePaths?: Set<string>
}

export function classifyEntry(
  name: string,
  type: 'file' | 'dir',
  openOrOpts?: boolean | ClassifyOptions,
): IconKind {
  const opts: ClassifyOptions =
    typeof openOrOpts === 'boolean' ? { open: openOrOpts } : (openOrOpts ?? {})
  if (type === 'dir') {
    return classifyFolder(name, opts.path ?? name, opts.open, opts.modulePaths)
  }
  return classifyFile(name)
}

const svgProps = {
  width: 16,
  height: 16,
  viewBox: '0 0 16 16',
  'aria-hidden': true as const,
}

/** Collapsed ▸ / expanded ▾ chevron. */
export function ChevronIcon({ open }: { open: boolean }) {
  return (
    <svg
      {...svgProps}
      width={12}
      height={12}
      className={`ij-chevron${open ? ' open' : ''}`}
    >
      {open ? (
        <path fill="currentColor" d="M4.5 6.25 8 9.75l3.5-3.5-.7-.7L8 8.35 5.2 5.55l-.7.7Z" />
      ) : (
        <path fill="currentColor" d="M6.25 4.5 9.75 8l-3.5 3.5-.7-.7L8.35 8 5.55 5.2l.7-.7Z" />
      )}
    </svg>
  )
}

export function TreeIcon({ kind }: { kind: IconKind }) {
  const src = ICON_URL[kind] ?? ICON_URL.anyType
  return (
    <img
      className={`ij-icon ij-icon-${kind}`}
      src={src}
      width={16}
      height={16}
      alt=""
      draggable={false}
    />
  )
}
