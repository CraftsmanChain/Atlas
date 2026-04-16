import { useState, useEffect, useMemo } from 'react';
import {
  Activity,
  ArrowLeft,
  Bell,
  BrainCircuit,
  CheckCircle2,
  ChevronDown,
  ChevronUp,
  FileJson,
  Filter,
  Globe,
  Moon,
  Monitor,
  Radar,
  Search,
  ServerCrash,
  ShieldAlert,
  Sun,
  Tags,
  Terminal,
  Zap,
} from 'lucide-react';
import { motion, AnimatePresence } from 'framer-motion';
import { useTranslation } from 'react-i18next';
import { useTheme } from 'next-themes';

type FailedIngestion = {
  id: number;
  source: string;
  level: string;
  message: string;
  process_status: string;
  callback_status: string;
  process_attempts: number;
  callback_attempts: number;
  process_last_error: string;
  callback_last_error: string;
  updated_at: string;
};

type IngestionRecord = {
  id: number;
  event_id: string;
  source: string;
  host: string;
  level: string;
  message: string;
  process_status: string;
  callback_status: string;
  raw_payload?: string;
  labels?: Record<string, string>;
  created_at: string;
  updated_at?: string;
  ai_report_id?: number;
  ai_report_status?: string;
  ai_report_summary?: string;
  ai_report_updated_at?: string;
  ai_report_confidence?: number;
};

type AnalysisReport = {
  id: number;
  ingestion_record_id: number;
  analysis_type: string;
  status: string;
  model: string;
  prompt_version: string;
  severity: string;
  summary: string;
  probable_causes?: string[];
  recommended_actions?: string[];
  evidence?: string[];
  confidence?: number;
  error_message?: string;
  updated_at: string;
};

function App() {
  const [activeTab, setActiveTab] = useState('alerts');
  const { t, i18n } = useTranslation();
  const { theme, setTheme } = useTheme();
  const [mounted, setMounted] = useState(false);
  const [failedIngestions, setFailedIngestions] = useState<FailedIngestion[]>([]);
  const [ingestions, setIngestions] = useState<IngestionRecord[]>([]);
  const [detailIngestionId, setDetailIngestionId] = useState<number | null>(null);
  const [analysisReport, setAnalysisReport] = useState<AnalysisReport | null>(null);
  const [analysisLoading, setAnalysisLoading] = useState(false);
  const [showAllLabels, setShowAllLabels] = useState(false);
  const [showRawPayload, setShowRawPayload] = useState(false);
  const [searchText, setSearchText] = useState('');
  const [hostFilter, setHostFilter] = useState('');
  const [sourceFilter, setSourceFilter] = useState('');
  const [levelFilter, setLevelFilter] = useState('');
  const [labelKeyFilter, setLabelKeyFilter] = useState('');
  const [labelValueFilter, setLabelValueFilter] = useState('');

  useEffect(() => {
    setMounted(true);
  }, []);

  useEffect(() => {
    let timer: number | undefined;
    const loadDashboard = async () => {
      try {
        const [ingestionResp, failureResp] = await Promise.all([
          fetch('/api/v1/alerts/ingestions?limit=100'),
          fetch('/api/v1/alerts/failures?limit=8'),
        ]);

        if (ingestionResp.ok) {
          const ingestionData = await ingestionResp.json();
          const items = Array.isArray(ingestionData.items) ? ingestionData.items as IngestionRecord[] : [];
          setIngestions(items);
          setDetailIngestionId((current) => {
            if (current && items.some((item) => item.id === current)) {
              return current;
            }
            return current && items.length === 0 ? null : current;
          });
        }

        if (failureResp.ok) {
          const failureData = await failureResp.json();
          setFailedIngestions(Array.isArray(failureData.items) ? failureData.items as FailedIngestion[] : []);
        }
      } catch {
        // 忽略开发阶段的瞬时网络错误，避免页面闪烁。
      } finally {
        timer = window.setTimeout(loadDashboard, 15000);
      }
    };

    loadDashboard();
    return () => {
      if (timer) window.clearTimeout(timer);
    };
  }, []);

  useEffect(() => {
    if (!detailIngestionId) {
      setAnalysisReport(null);
      return;
    }

    let cancelled = false;
    const loadAnalysis = async () => {
      setAnalysisLoading(true);
      try {
        const resp = await fetch(`/api/v1/alerts/ingestions/${detailIngestionId}/analysis`);
        if (!resp.ok) {
          if (!cancelled) setAnalysisReport(null);
          return;
        }
        const data = await resp.json();
        if (!cancelled) setAnalysisReport(data as AnalysisReport);
      } catch {
        if (!cancelled) setAnalysisReport(null);
      } finally {
        if (!cancelled) setAnalysisLoading(false);
      }
    };

    loadAnalysis();
    return () => {
      cancelled = true;
    };
  }, [detailIngestionId]);

  const toggleLanguage = () => {
    const newLang = i18n.language === 'zh' ? 'en' : 'zh';
    i18n.changeLanguage(newLang);
    localStorage.setItem('atlas_lang', newLang);
  };

  const selectedIngestion = useMemo(
    () => ingestions.find((item) => item.id === detailIngestionId) ?? null,
    [ingestions, detailIngestionId],
  );

  const hostOptions = useMemo(
    () => Array.from(new Set(ingestions.map((item) => item.host).filter(Boolean))).sort(),
    [ingestions],
  );

  const sourceOptions = useMemo(
    () => Array.from(new Set(ingestions.map((item) => item.source).filter(Boolean))).sort(),
    [ingestions],
  );

  const filteredIngestions = useMemo(() => {
    const query = searchText.trim().toLowerCase();
    const labelKey = labelKeyFilter.trim().toLowerCase();
    const labelValue = labelValueFilter.trim().toLowerCase();

    return ingestions.filter((item) => {
      if (hostFilter && item.host !== hostFilter) return false;
      if (sourceFilter && item.source !== sourceFilter) return false;
      if (levelFilter && item.level !== levelFilter) return false;

      const labels = item.labels ?? {};
      const labelEntries = Object.entries(labels);

      if (labelKey) {
        const matchedByKey = labelEntries.some(([key]) => key.toLowerCase().includes(labelKey));
        if (!matchedByKey) return false;
      }

      if (labelValue) {
        const matchedByValue = labelEntries.some(([, value]) => String(value).toLowerCase().includes(labelValue));
        if (!matchedByValue) return false;
      }

      if (!query) return true;

      const searchableParts = [
        item.message,
        item.host,
        item.source,
        item.level,
        item.raw_payload,
        ...labelEntries.flatMap(([key, value]) => [key, String(value)]),
      ]
        .filter(Boolean)
        .join(' ')
        .toLowerCase();

      return searchableParts.includes(query);
    });
  }, [ingestions, searchText, hostFilter, sourceFilter, levelFilter, labelKeyFilter, labelValueFilter]);

  const stats = [
    { id: 'received', value: String(ingestions.length), icon: Bell, color: 'text-accent', bg: 'bg-accent/10' },
    { id: 'analysisReady', value: String(ingestions.filter((item) => item.ai_report_status === 'completed').length), icon: BrainCircuit, color: 'text-primary', bg: 'bg-primary/10' },
    { id: 'failedCount', value: String(failedIngestions.length), icon: ServerCrash, color: 'text-warning', bg: 'bg-warning/10' },
    { id: 'hosts', value: String(hostOptions.length), icon: Radar, color: 'text-secondary', bg: 'bg-secondary/10' },
  ];

  const getLevelColor = (level: string) => {
    switch (level) {
      case 'critical':
        return 'text-accent border-accent/30 bg-accent/10';
      case 'error':
        return 'text-orange-400 border-orange-400/30 bg-orange-400/10';
      case 'warning':
        return 'text-warning border-warning/30 bg-warning/10';
      case 'info':
        return 'text-primary border-primary/30 bg-primary/10';
      default:
        return 'text-textMuted border-border bg-surface';
    }
  };

  const getLevelIcon = (level: string) => {
    switch (level) {
      case 'critical':
        return <ShieldAlert className="w-4 h-4 text-accent" />;
      case 'error':
        return <ShieldAlert className="w-4 h-4 text-orange-400" />;
      case 'warning':
        return <Zap className="w-4 h-4 text-warning" />;
      case 'info':
        return <CheckCircle2 className="w-4 h-4 text-primary" />;
      default:
        return <Activity className="w-4 h-4 text-textMuted" />;
    }
  };

  const getAiStatusColor = (status?: string) => {
    switch (status) {
      case 'completed':
        return 'text-success bg-success/10 border-success/20';
      case 'blocked':
        return 'text-accent bg-accent/10 border-accent/20';
      case 'failed':
        return 'text-orange-400 bg-orange-400/10 border-orange-400/20';
      default:
        return 'text-textMuted bg-surface border-border';
    }
  };

  const formatTime = (value?: string) => {
    if (!value) return '-';
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return value;
    return new Intl.DateTimeFormat(i18n.language === 'zh' ? 'zh-CN' : 'en-US', {
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    }).format(date);
  };

  const openDetail = (recordId: number) => {
    setDetailIngestionId(recordId);
    setShowAllLabels(false);
    setShowRawPayload(false);
  };

  const clearFilters = () => {
    setSearchText('');
    setHostFilter('');
    setSourceFilter('');
    setLevelFilter('');
    setLabelKeyFilter('');
    setLabelValueFilter('');
  };

  const visibleLabelEntries = Object.entries(selectedIngestion?.labels ?? {});
  const detailLabelEntries = showAllLabels ? visibleLabelEntries : visibleLabelEntries.slice(0, 16);

  if (!mounted) return null;

  const renderHeader = () => (
    <header className="flex items-center justify-between mb-12 border-b border-border/50 pb-6">
      <div className="flex items-center space-x-4">
        <div className="w-12 h-12 glass-panel rounded-xl flex items-center justify-center neon-border">
          <Terminal className="w-6 h-6 text-primary text-glow" />
        </div>
        <div>
          <h1 className="text-2xl font-bold tracking-tight text-textMain flex items-center gap-2">
            {t('header.title')} <span className="text-primary font-mono text-sm font-normal px-2 py-0.5 bg-primary/10 border border-primary/20 rounded">v1.0.0</span>
          </h1>
          <p className="text-textMuted text-sm font-mono mt-1">{t('header.subtitle')}</p>
        </div>
      </div>

      <div className="flex items-center space-x-4">
        <div className="flex space-x-1 glass-panel p-1 rounded-lg">
          <button
            type="button"
            onClick={() => setTheme('light')}
            title={t('theme.light')}
            className={`p-2 rounded-md transition-all duration-200 ${theme === 'light' ? 'bg-surfaceHover text-primary shadow-sm border border-black/5 dark:border-white/10' : 'text-textMuted hover:text-textMain hover:bg-surface/50'}`}
          >
            <Sun className="w-4 h-4" />
          </button>
          <button
            type="button"
            onClick={() => setTheme('dark')}
            title={t('theme.dark')}
            className={`p-2 rounded-md transition-all duration-200 ${theme === 'dark' ? 'bg-surfaceHover text-primary shadow-sm border border-black/5 dark:border-white/10' : 'text-textMuted hover:text-textMain hover:bg-surface/50'}`}
          >
            <Moon className="w-4 h-4" />
          </button>
          <button
            type="button"
            onClick={() => setTheme('system')}
            title={t('theme.system')}
            className={`p-2 rounded-md transition-all duration-200 ${theme === 'system' ? 'bg-surfaceHover text-primary shadow-sm border border-black/5 dark:border-white/10' : 'text-textMuted hover:text-textMain hover:bg-surface/50'}`}
          >
            <Monitor className="w-4 h-4" />
          </button>
        </div>

        <button
          type="button"
          onClick={toggleLanguage}
          className="flex items-center gap-2 px-3 py-2 glass-panel rounded-lg text-sm font-medium text-textMuted hover:text-textMain transition-all"
        >
          <Globe className="w-4 h-4" />
          <span>{i18n.language === 'zh' ? 'EN' : '中文'}</span>
        </button>

        <nav className="flex space-x-2 glass-panel p-1 rounded-lg ml-4">
          {['alerts', 'metrics', 'config'].map((tab) => (
            <button
              key={tab}
              type="button"
              onClick={() => setActiveTab(tab)}
              className={`px-4 py-2 rounded-md text-sm font-medium transition-all duration-200 ${
                activeTab === tab
                  ? 'bg-surfaceHover text-textMain shadow-sm border border-black/5 dark:border-white/10'
                  : 'text-textMuted hover:text-textMain hover:bg-surface/50'
              }`}
            >
              {t(`nav.${tab}`)}
            </button>
          ))}
        </nav>
      </div>
    </header>
  );

  const renderStats = () => (
    <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
      {stats.map((stat, i) => (
        <motion.div
          key={stat.id}
          initial={{ opacity: 0, y: 20 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ delay: i * 0.1 }}
          className="glass-panel p-6 rounded-2xl relative overflow-hidden group"
        >
          <div className={`absolute top-0 right-0 w-32 h-32 bg-gradient-to-br from-transparent to-current opacity-5 -mr-10 -mt-10 rounded-full transition-transform group-hover:scale-150 duration-500 ${stat.color}`}></div>
          <div className="flex items-center justify-between">
            <div>
              <p className="text-sm font-medium text-textMuted mb-1">{t(`stats.${stat.id}`)}</p>
              <h3 className="text-3xl font-bold text-textMain font-mono">{stat.value}</h3>
            </div>
            <div className={`w-12 h-12 rounded-xl flex items-center justify-center ${stat.bg} ${stat.color} border border-current/20`}>
              <stat.icon className="w-6 h-6" />
            </div>
          </div>
        </motion.div>
      ))}
    </div>
  );

  const renderSidebar = () => (
    <div className="space-y-8">
      <div className="glass-panel p-6 rounded-2xl border-t-4 border-t-primary/50">
        <h3 className="text-sm font-medium text-textMuted mb-4 uppercase tracking-wider">{t('status.title')}</h3>
        <div className="space-y-4 font-mono text-sm">
          <div className="flex justify-between items-center">
            <span className="text-textMuted">{t('status.db')}</span>
            <span className="text-success">{t('status.connected')}</span>
          </div>
          <div className="flex justify-between items-center">
            <span className="text-textMuted">{t('status.bot')}</span>
            <span className="text-success">{t('status.active', { count: 2 })}</span>
          </div>
          <div className="flex justify-between items-center">
            <span className="text-textMuted">{t('status.uptime')}</span>
            <span className="text-textMain">14d 2h 45m</span>
          </div>
          <div className="w-full bg-surface h-1.5 rounded-full overflow-hidden mt-2 border border-border/50">
            <div className="bg-primary h-full w-[99%] shadow-[0_0_12px_var(--glow-color)]"></div>
          </div>
        </div>
      </div>

      <div className="glass-panel p-6 rounded-2xl border-t-4 border-t-accent/50">
        <h3 className="text-sm font-medium text-textMuted mb-4 uppercase tracking-wider">
          {t('ingestionFailures.title')}
        </h3>
        <p className="text-xs text-textMuted mb-3">
          {t('ingestionFailures.count', { count: failedIngestions.length })}
        </p>
        <div className="space-y-3 max-h-64 overflow-y-auto">
          {failedIngestions.length === 0 ? (
            <div className="text-xs text-success">{t('ingestionFailures.none')}</div>
          ) : (
            failedIngestions.map((item) => (
              <div key={item.id} className="rounded-lg border border-border/60 bg-surface/40 p-3">
                <div className="flex items-center justify-between gap-2">
                  <span className="text-xs text-textMain truncate">{item.message}</span>
                  <span className={`text-[10px] uppercase px-2 py-0.5 rounded ${getLevelColor(item.level)}`}>{item.level || 'unknown'}</span>
                </div>
                <div className="mt-1 text-[11px] text-textMuted">
                  {t('alerts.source')}: {item.source || '-'} | #{item.id}
                </div>
                <div className="mt-1 text-[11px] text-accent break-words">
                  {item.process_status === 'failed' ? item.process_last_error : item.callback_last_error}
                </div>
              </div>
            ))
          )}
        </div>
      </div>
    </div>
  );

  const renderListPage = () => (
    <div className="grid grid-cols-1 lg:grid-cols-4 gap-8">
      <div className="lg:col-span-3 space-y-6">
        <div className="glass-panel rounded-2xl p-5 border border-border/60">
          <div className="flex items-center justify-between gap-4 mb-4">
            <div>
              <h2 className="text-lg font-semibold text-textMain flex items-center gap-2">
                <Activity className="w-5 h-5 text-primary" />
                {t('ingestions.title')}
              </h2>
              <p className="text-xs text-textMuted mt-1">
                {t('ingestions.filteredCount', { filtered: filteredIngestions.length, total: ingestions.length })}
              </p>
            </div>
            <span className="flex items-center gap-2 text-xs font-mono text-success">
              <span className="relative flex h-2 w-2">
                <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-success opacity-75"></span>
                <span className="relative inline-flex rounded-full h-2 w-2 bg-success"></span>
              </span>
              {t('ingestions.connected')}
            </span>
          </div>

          <div className="grid grid-cols-1 xl:grid-cols-2 gap-3">
            <label className="rounded-xl border border-border/60 bg-surface/30 px-3 py-2 flex items-center gap-2">
              <Search className="w-4 h-4 text-textMuted" />
              <input
                value={searchText}
                onChange={(e) => setSearchText(e.target.value)}
                placeholder={t('ingestions.searchPlaceholder')}
                className="w-full bg-transparent outline-none text-sm text-textMain placeholder:text-textMuted"
              />
            </label>

            <div className="grid grid-cols-2 gap-3">
              <select
                aria-label={t('ingestions.filters.allHosts')}
                title={t('ingestions.filters.allHosts')}
                value={hostFilter}
                onChange={(e) => setHostFilter(e.target.value)}
                className="rounded-xl border border-border/60 bg-surface/30 px-3 py-2 text-sm text-textMain outline-none"
              >
                <option value="">{t('ingestions.filters.allHosts')}</option>
                {hostOptions.map((host) => (
                  <option key={host} value={host}>{host}</option>
                ))}
              </select>

              <select
                aria-label={t('ingestions.filters.allSources')}
                title={t('ingestions.filters.allSources')}
                value={sourceFilter}
                onChange={(e) => setSourceFilter(e.target.value)}
                className="rounded-xl border border-border/60 bg-surface/30 px-3 py-2 text-sm text-textMain outline-none"
              >
                <option value="">{t('ingestions.filters.allSources')}</option>
                {sourceOptions.map((source) => (
                  <option key={source} value={source}>{source}</option>
                ))}
              </select>
            </div>

            <div className="grid grid-cols-3 gap-3 xl:col-span-2">
              <select
                aria-label={t('ingestions.filters.allLevels')}
                title={t('ingestions.filters.allLevels')}
                value={levelFilter}
                onChange={(e) => setLevelFilter(e.target.value)}
                className="rounded-xl border border-border/60 bg-surface/30 px-3 py-2 text-sm text-textMain outline-none"
              >
                <option value="">{t('ingestions.filters.allLevels')}</option>
                <option value="critical">critical</option>
                <option value="error">error</option>
                <option value="warning">warning</option>
                <option value="info">info</option>
              </select>

              <label className="rounded-xl border border-border/60 bg-surface/30 px-3 py-2 flex items-center gap-2">
                <Tags className="w-4 h-4 text-textMuted" />
                <input
                  value={labelKeyFilter}
                  onChange={(e) => setLabelKeyFilter(e.target.value)}
                  placeholder={t('ingestions.filters.labelKey')}
                  className="w-full bg-transparent outline-none text-sm text-textMain placeholder:text-textMuted"
                />
              </label>

              <label className="rounded-xl border border-border/60 bg-surface/30 px-3 py-2 flex items-center gap-2">
                <Filter className="w-4 h-4 text-textMuted" />
                <input
                  value={labelValueFilter}
                  onChange={(e) => setLabelValueFilter(e.target.value)}
                  placeholder={t('ingestions.filters.labelValue')}
                  className="w-full bg-transparent outline-none text-sm text-textMain placeholder:text-textMuted"
                />
              </label>
            </div>
          </div>

          <div className="mt-4 flex items-center justify-between gap-3">
            <div className="text-xs text-textMuted">
              {t('ingestions.filters.summary', {
                host: hostFilter || t('ingestions.filters.any'),
                source: sourceFilter || t('ingestions.filters.any'),
                level: levelFilter || t('ingestions.filters.any'),
              })}
            </div>
            <button
              type="button"
              onClick={clearFilters}
              className="text-xs px-3 py-1.5 rounded-lg border border-border/60 bg-surface/40 text-textMain hover:text-primary hover:border-primary/40 transition-colors"
            >
              {t('ingestions.filters.clear')}
            </button>
          </div>
        </div>

        <div className="glass-panel rounded-2xl overflow-hidden flex flex-col min-h-[560px]">
          <div className="flex-1 overflow-y-auto p-4 space-y-3">
            <AnimatePresence>
              {filteredIngestions.length === 0 ? (
                <div className="h-full flex items-center justify-center text-sm text-textMuted">
                  {t('ingestions.none')}
                </div>
              ) : filteredIngestions.map((alert, i) => {
                const labelEntries = Object.entries(alert.labels ?? {});
                const previewLabels = labelEntries.slice(0, 4);
                return (
                  <motion.button
                    key={alert.id}
                    type="button"
                    initial={{ opacity: 0, x: -20 }}
                    animate={{ opacity: 1, x: 0 }}
                    transition={{ delay: 0.04 * i }}
                    onClick={() => openDetail(alert.id)}
                    className="w-full text-left group flex items-start gap-4 p-4 rounded-xl border border-border/50 bg-surface/40 hover:bg-surfaceHover/80 hover:border-primary/40 transition-colors"
                  >
                    <div className={`mt-1 p-2 rounded-lg border ${getLevelColor(alert.level)}`}>
                      {getLevelIcon(alert.level)}
                    </div>
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center justify-between mb-1 gap-3">
                        <span className="text-sm font-medium text-textMain truncate">{alert.message}</span>
                        <span className="text-xs font-mono text-textMuted whitespace-nowrap">{formatTime(alert.created_at)}</span>
                      </div>
                      <div className="flex flex-wrap items-center gap-3 text-xs">
                        <span className="text-textMuted">{t('alerts.source')}: <span className="text-textMain">{alert.source}</span></span>
                        <span className="text-border">|</span>
                        <span className={`px-2 py-0.5 rounded font-mono uppercase tracking-wider ${getLevelColor(alert.level)} border-none bg-transparent p-0`}>
                          {alert.level}
                        </span>
                        <span className="text-border">|</span>
                        <span className="text-textMuted">{t('ingestions.host')}: <span className="text-textMain">{alert.host || '-'}</span></span>
                      </div>
                      <div className="mt-3 flex flex-wrap gap-2">
                        {previewLabels.map(([key, value]) => (
                          <span key={key} className="rounded-full border border-border/60 bg-surface/40 px-2.5 py-1 text-[11px] text-textMain font-mono">
                            {key}={value}
                          </span>
                        ))}
                        {labelEntries.length > previewLabels.length && (
                          <span className="rounded-full border border-primary/30 bg-primary/10 px-2.5 py-1 text-[11px] text-primary font-mono">
                            +{labelEntries.length - previewLabels.length} {t('ingestions.moreLabels')}
                          </span>
                        )}
                      </div>
                      <div className="mt-3 flex items-center gap-2">
                        <span className={`text-[10px] px-2 py-0.5 rounded border font-mono uppercase ${getAiStatusColor(alert.ai_report_status)}`}>
                          {alert.ai_report_status || 'pending'}
                        </span>
                        <span className="text-[11px] text-textMuted truncate">
                          {alert.ai_report_summary || t('analysis.pending')}
                        </span>
                      </div>
                    </div>
                  </motion.button>
                );
              })}
            </AnimatePresence>
          </div>
        </div>
      </div>

      {renderSidebar()}
    </div>
  );

  const renderDetailPage = () => (
    <div className="grid grid-cols-1 lg:grid-cols-4 gap-8">
      <div className="lg:col-span-3 space-y-6">
        <div className="flex items-center justify-between gap-4">
          <button
            type="button"
            onClick={() => setDetailIngestionId(null)}
            className="inline-flex items-center gap-2 rounded-xl border border-border/60 bg-surface/40 px-4 py-2 text-sm text-textMain hover:text-primary hover:border-primary/40 transition-colors"
          >
            <ArrowLeft className="w-4 h-4" />
            {t('detail.back')}
          </button>
          <div className="text-xs font-mono text-textMuted">
            #{selectedIngestion?.id} · {selectedIngestion?.source} · {formatTime(selectedIngestion?.created_at)}
          </div>
        </div>

        <div className="glass-panel rounded-2xl p-6 border border-border/60">
          <div className="flex flex-wrap items-start justify-between gap-4">
            <div className="space-y-3">
              <div className="flex items-center gap-3">
                <div className={`p-2 rounded-lg border ${getLevelColor(selectedIngestion?.level || '')}`}>
                  {getLevelIcon(selectedIngestion?.level || '')}
                </div>
                <div>
                  <h2 className="text-xl font-semibold text-textMain break-words">{selectedIngestion?.message}</h2>
                  <div className="mt-1 text-sm text-textMuted">
                    {t('alerts.source')}: {selectedIngestion?.source || '-'} · {t('ingestions.host')}: {selectedIngestion?.host || '-'}
                  </div>
                </div>
              </div>
              <div className="flex flex-wrap gap-2">
                <span className={`text-[10px] px-2 py-0.5 rounded border font-mono uppercase ${getLevelColor(selectedIngestion?.level || '')}`}>
                  {selectedIngestion?.level || 'unknown'}
                </span>
                <span className={`text-[10px] px-2 py-0.5 rounded border font-mono uppercase ${getAiStatusColor(selectedIngestion?.ai_report_status)}`}>
                  {selectedIngestion?.ai_report_status || 'pending'}
                </span>
                <span className="text-[10px] px-2 py-0.5 rounded border border-border/60 bg-surface/40 text-textMuted font-mono uppercase">
                  {selectedIngestion?.process_status || 'processing'}
                </span>
              </div>
            </div>

            <div className="grid grid-cols-2 gap-3 min-w-[260px]">
              <div className="rounded-xl border border-border/60 bg-surface/30 p-3">
                <div className="text-[11px] uppercase tracking-wider text-textMuted">{t('detail.receivedAt')}</div>
                <div className="mt-1 text-sm font-mono text-textMain">{formatTime(selectedIngestion?.created_at)}</div>
              </div>
              <div className="rounded-xl border border-border/60 bg-surface/30 p-3">
                <div className="text-[11px] uppercase tracking-wider text-textMuted">{t('detail.analysisId')}</div>
                <div className="mt-1 text-sm font-mono text-textMain">{selectedIngestion?.ai_report_id || '-'}</div>
              </div>
            </div>
          </div>
        </div>

        <div className="glass-panel p-6 rounded-2xl border-t-4 border-t-warning/50">
          <h3 className="text-sm font-medium text-textMuted mb-4 uppercase tracking-wider">
            {t('analysis.title')}
          </h3>
          {analysisLoading ? (
            <div className="text-sm text-textMuted">{t('analysis.loading')}</div>
          ) : !analysisReport ? (
            <div className="text-sm text-textMuted">{t('analysis.pending')}</div>
          ) : (
            <div className="space-y-5">
              <div className="flex items-start justify-between gap-4">
                <div>
                  <div className="text-base text-textMain font-semibold">{analysisReport.summary || t('analysis.pending')}</div>
                  <div className="mt-1 text-[11px] text-textMuted font-mono">
                    {analysisReport.model} · {analysisReport.prompt_version}
                  </div>
                </div>
                <span className={`text-[10px] px-2 py-1 rounded border font-mono uppercase ${getAiStatusColor(analysisReport.status)}`}>
                  {analysisReport.status}
                </span>
              </div>

              <div className="grid grid-cols-1 sm:grid-cols-3 gap-3 text-xs">
                <div className="rounded-xl border border-border/60 bg-surface/40 p-3">
                  <div className="text-textMuted">{t('analysis.confidence')}</div>
                  <div className="mt-1 text-lg font-mono text-textMain">{Math.round((analysisReport.confidence ?? 0) * 100)}%</div>
                </div>
                <div className="rounded-xl border border-border/60 bg-surface/40 p-3">
                  <div className="text-textMuted">{t('analysis.updated')}</div>
                  <div className="mt-1 text-sm font-mono text-textMain">{formatTime(analysisReport.updated_at)}</div>
                </div>
                <div className="rounded-xl border border-border/60 bg-surface/40 p-3">
                  <div className="text-textMuted">{t('detail.analysisType')}</div>
                  <div className="mt-1 text-sm font-mono text-textMain">{analysisReport.analysis_type}</div>
                </div>
              </div>

              <div>
                <div className="text-[11px] uppercase tracking-wider text-textMuted mb-2">{t('analysis.causes')}</div>
                <div className="space-y-2">
                  {(analysisReport.probable_causes ?? []).map((item, index) => (
                    <div key={index} className="text-sm text-textMain rounded-lg border border-border/50 bg-surface/30 px-3 py-2">{item}</div>
                  ))}
                </div>
              </div>

              <div>
                <div className="text-[11px] uppercase tracking-wider text-textMuted mb-2">{t('analysis.actions')}</div>
                <div className="space-y-2">
                  {(analysisReport.recommended_actions ?? []).map((item, index) => (
                    <div key={index} className="text-sm text-textMain rounded-lg border border-border/50 bg-surface/30 px-3 py-2">{item}</div>
                  ))}
                </div>
              </div>

              <div>
                <div className="text-[11px] uppercase tracking-wider text-textMuted mb-2">{t('analysis.evidence')}</div>
                <div className="space-y-2">
                  {(analysisReport.evidence ?? []).map((item, index) => (
                    <div key={index} className="text-xs text-textMuted rounded-lg border border-border/50 bg-surface/20 px-3 py-2 font-mono break-all">{item}</div>
                  ))}
                </div>
              </div>

              {analysisReport.error_message && (
                <div className="rounded-xl border border-accent/30 bg-accent/10 p-3 text-sm text-accent">
                  {analysisReport.error_message}
                </div>
              )}
            </div>
          )}
        </div>

        <div className="glass-panel p-6 rounded-2xl border-t-4 border-t-secondary/50">
          <div className="flex items-center justify-between gap-3 mb-4">
            <h3 className="text-sm font-medium text-textMuted uppercase tracking-wider">
              {t('ingestions.detail')}
            </h3>
            <button
              type="button"
              onClick={() => setShowAllLabels((current) => !current)}
              className="inline-flex items-center gap-1 text-xs px-3 py-1.5 rounded-lg border border-border/60 bg-surface/40 text-textMain hover:text-primary hover:border-primary/40 transition-colors"
            >
              {showAllLabels ? <ChevronUp className="w-3.5 h-3.5" /> : <ChevronDown className="w-3.5 h-3.5" />}
              {showAllLabels ? t('ingestions.collapseLabels') : t('ingestions.expandLabels')}
            </button>
          </div>
          <div className="flex flex-wrap gap-2">
            {detailLabelEntries.length === 0 ? (
              <span className="text-[11px] text-textMuted">{t('ingestions.noLabels')}</span>
            ) : (
              detailLabelEntries.map(([key, value]) => (
                <span key={key} className="rounded-full border border-border/60 bg-surface/40 px-2.5 py-1 text-[11px] text-textMain font-mono break-all">
                  {key}={value}
                </span>
              ))
            )}
          </div>
          {!showAllLabels && visibleLabelEntries.length > detailLabelEntries.length && (
            <div className="mt-3 text-xs text-textMuted">
              {t('ingestions.remainingLabels', { count: visibleLabelEntries.length - detailLabelEntries.length })}
            </div>
          )}
        </div>

        <div className="glass-panel p-6 rounded-2xl border-t-4 border-t-primary/50">
          <div className="flex items-center justify-between gap-3 mb-4">
            <h3 className="text-sm font-medium text-textMuted uppercase tracking-wider flex items-center gap-2">
              <FileJson className="w-4 h-4" />
              {t('detail.rawPayload')}
            </h3>
            <button
              type="button"
              onClick={() => setShowRawPayload((current) => !current)}
              className="inline-flex items-center gap-1 text-xs px-3 py-1.5 rounded-lg border border-border/60 bg-surface/40 text-textMain hover:text-primary hover:border-primary/40 transition-colors"
            >
              {showRawPayload ? <ChevronUp className="w-3.5 h-3.5" /> : <ChevronDown className="w-3.5 h-3.5" />}
              {showRawPayload ? t('detail.hidePayload') : t('detail.viewPayload')}
            </button>
          </div>
          {showRawPayload ? (
            <pre className="rounded-xl border border-border/60 bg-surface/30 p-4 text-xs text-textMain overflow-x-auto whitespace-pre-wrap break-all font-mono">
              {selectedIngestion?.raw_payload || t('detail.noPayload')}
            </pre>
          ) : (
            <div className="text-sm text-textMuted">{t('detail.payloadCollapsed')}</div>
          )}
        </div>
      </div>

      {renderSidebar()}
    </div>
  );

  const renderPlaceholder = () => (
    <div className="glass-panel rounded-2xl p-10 text-center border border-border/60">
      <div className="text-lg font-semibold text-textMain">{t('panels.comingSoon')}</div>
      <div className="mt-2 text-sm text-textMuted">{t('panels.hint')}</div>
    </div>
  );

  return (
    <div className="min-h-screen bg-background bg-grid relative overflow-hidden transition-colors duration-300">
      <div className="absolute top-[-20%] left-[-10%] w-[50%] h-[50%] bg-glow-primary rounded-full blur-[120px] pointer-events-none opacity-50 transition-opacity duration-300"></div>
      <div className="absolute bottom-[-20%] right-[-10%] w-[40%] h-[40%] bg-secondary/10 rounded-full blur-[100px] pointer-events-none opacity-30 transition-opacity duration-300"></div>

      <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8 relative z-10">
        {renderHeader()}

        <main className="space-y-8">
          {renderStats()}
          {activeTab !== 'alerts' ? renderPlaceholder() : detailIngestionId ? renderDetailPage() : renderListPage()}
        </main>
      </div>
    </div>
  );
}

export default App;
