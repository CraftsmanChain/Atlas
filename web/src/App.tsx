import { useState, useEffect } from 'react';
import { Activity, Bell, Terminal, Zap, ShieldAlert, Cpu, Network, CheckCircle2, Moon, Sun, Monitor, Globe } from 'lucide-react';
import { motion, AnimatePresence } from 'framer-motion';
import { useTranslation } from 'react-i18next';
import { useTheme } from 'next-themes';

function App() {
  const [activeTab, setActiveTab] = useState('alerts');
  const { t, i18n } = useTranslation();
  const { theme, setTheme } = useTheme();
  const [mounted, setMounted] = useState(false);
  const [failedIngestions, setFailedIngestions] = useState<Array<{
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
  }>>([]);

  // 避免 hydration mismatch
  useEffect(() => {
    setMounted(true);
  }, []);

  useEffect(() => {
    let timer: number | undefined;
    const loadFailures = async () => {
      try {
        const resp = await fetch('/api/v1/alerts/failures?limit=8');
        if (!resp.ok) return;
        const data = await resp.json();
        setFailedIngestions(Array.isArray(data.items) ? data.items : []);
      } catch {
        // 前端独立开发模式下，后端未联通时忽略错误，避免页面抖动。
      } finally {
        timer = window.setTimeout(loadFailures, 15000);
      }
    };
    loadFailures();
    return () => {
      if (timer) window.clearTimeout(timer);
    };
  }, []);

  const toggleLanguage = () => {
    const newLang = i18n.language === 'zh' ? 'en' : 'zh';
    i18n.changeLanguage(newLang);
    localStorage.setItem('atlas_lang', newLang);
  };

  // 模拟数据
  const stats = [
    { id: 'activeAlerts', value: '12', icon: Bell, color: 'text-accent', bg: 'bg-accent/10' },
    { id: 'events24h', value: '3,492', icon: Activity, color: 'text-primary', bg: 'bg-primary/10' },
    { id: 'systemLoad', value: '42%', icon: Cpu, color: 'text-warning', bg: 'bg-warning/10' },
    { id: 'networkIO', value: '1.2 GB/s', icon: Network, color: 'text-secondary', bg: 'bg-secondary/10' },
  ];

  const recentAlerts = [
    { id: '1', level: 'critical', message: 'High CPU Load on web-server-01', source: 'Prometheus', time: '2 mins ago' },
    { id: '2', level: 'warning', message: 'Memory usage > 80% on db-master', source: 'Node Exporter', time: '15 mins ago' },
    { id: '3', level: 'info', message: 'Backup job completed successfully', source: 'Cron', time: '1 hour ago' },
    { id: '4', level: 'critical', message: 'Connection timeout to redis-cluster', source: 'App', time: '2 hours ago' },
  ];

  const getLevelColor = (level: string) => {
    switch (level) {
      case 'critical': return 'text-accent border-accent/30 bg-accent/10';
      case 'warning': return 'text-warning border-warning/30 bg-warning/10';
      case 'info': return 'text-primary border-primary/30 bg-primary/10';
      default: return 'text-textMuted border-border bg-surface';
    }
  };

  const getLevelIcon = (level: string) => {
    switch (level) {
      case 'critical': return <ShieldAlert className="w-4 h-4 text-accent" />;
      case 'warning': return <Zap className="w-4 h-4 text-warning" />;
      case 'info': return <CheckCircle2 className="w-4 h-4 text-primary" />;
      default: return <Activity className="w-4 h-4 text-textMuted" />;
    }
  };

  if (!mounted) return null;

  return (
    <div className="min-h-screen bg-background bg-grid relative overflow-hidden transition-colors duration-300">
      {/* Background glow effects */}
      <div className="absolute top-[-20%] left-[-10%] w-[50%] h-[50%] bg-glow-primary rounded-full blur-[120px] pointer-events-none opacity-50 transition-opacity duration-300"></div>
      <div className="absolute bottom-[-20%] right-[-10%] w-[40%] h-[40%] bg-secondary/10 rounded-full blur-[100px] pointer-events-none opacity-30 transition-opacity duration-300"></div>

      <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8 relative z-10">
        {/* Header */}
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
            {/* Theme Toggle */}
            <div className="flex space-x-1 glass-panel p-1 rounded-lg">
              <button
                onClick={() => setTheme('light')}
                title={t('theme.light')}
                className={`p-2 rounded-md transition-all duration-200 ${theme === 'light' ? 'bg-surfaceHover text-primary shadow-sm border border-black/5 dark:border-white/10' : 'text-textMuted hover:text-textMain hover:bg-surface/50'}`}
              >
                <Sun className="w-4 h-4" />
              </button>
              <button
                onClick={() => setTheme('dark')}
                title={t('theme.dark')}
                className={`p-2 rounded-md transition-all duration-200 ${theme === 'dark' ? 'bg-surfaceHover text-primary shadow-sm border border-black/5 dark:border-white/10' : 'text-textMuted hover:text-textMain hover:bg-surface/50'}`}
              >
                <Moon className="w-4 h-4" />
              </button>
              <button
                onClick={() => setTheme('system')}
                title={t('theme.system')}
                className={`p-2 rounded-md transition-all duration-200 ${theme === 'system' ? 'bg-surfaceHover text-primary shadow-sm border border-black/5 dark:border-white/10' : 'text-textMuted hover:text-textMain hover:bg-surface/50'}`}
              >
                <Monitor className="w-4 h-4" />
              </button>
            </div>

            {/* Language Toggle */}
            <button
              onClick={toggleLanguage}
              className="flex items-center gap-2 px-3 py-2 glass-panel rounded-lg text-sm font-medium text-textMuted hover:text-textMain transition-all"
            >
              <Globe className="w-4 h-4" />
              <span>{i18n.language === 'zh' ? 'EN' : '中文'}</span>
            </button>

            {/* Navigation Tabs */}
            <nav className="flex space-x-2 glass-panel p-1 rounded-lg ml-4">
              {['alerts', 'metrics', 'config'].map((tab) => (
                <button
                  key={tab}
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

        <main className="space-y-8">
          {/* Stats Grid */}
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

          {/* Main Content Area */}
          <div className="grid grid-cols-1 lg:grid-cols-3 gap-8">
            {/* Alert Stream */}
            <div className="lg:col-span-2 space-y-4">
              <div className="flex items-center justify-between mb-2">
                <h2 className="text-lg font-semibold text-textMain flex items-center gap-2">
                  <Activity className="w-5 h-5 text-primary" />
                  {t('alerts.title')}
                </h2>
                <span className="flex items-center gap-2 text-xs font-mono text-success">
                  <span className="relative flex h-2 w-2">
                    <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-success opacity-75"></span>
                    <span className="relative inline-flex rounded-full h-2 w-2 bg-success"></span>
                  </span>
                  {t('alerts.connected')}
                </span>
              </div>
              
              <div className="glass-panel rounded-2xl overflow-hidden flex flex-col h-[500px]">
                <div className="flex-1 overflow-y-auto p-4 space-y-3">
                  <AnimatePresence>
                    {recentAlerts.map((alert, i) => (
                      <motion.div 
                        key={alert.id}
                        initial={{ opacity: 0, x: -20 }}
                        animate={{ opacity: 1, x: 0 }}
                        transition={{ delay: 0.3 + i * 0.1 }}
                        className="group flex items-start gap-4 p-4 rounded-xl border border-border/50 bg-surface/40 hover:bg-surfaceHover/80 transition-colors"
                      >
                        <div className={`mt-1 p-2 rounded-lg border ${getLevelColor(alert.level)}`}>
                          {getLevelIcon(alert.level)}
                        </div>
                        <div className="flex-1 min-w-0">
                          <div className="flex items-center justify-between mb-1">
                            <span className="text-sm font-medium text-textMain truncate">{alert.message}</span>
                            <span className="text-xs font-mono text-textMuted whitespace-nowrap ml-4">{alert.time}</span>
                          </div>
                          <div className="flex items-center gap-3 text-xs">
                            <span className="text-textMuted">{t('alerts.source')}: <span className="text-textMain">{alert.source}</span></span>
                            <span className="text-border">|</span>
                            <span className={`px-2 py-0.5 rounded font-mono uppercase tracking-wider ${getLevelColor(alert.level)} border-none bg-transparent p-0`}>
                              {alert.level}
                            </span>
                          </div>
                        </div>
                      </motion.div>
                    ))}
                  </AnimatePresence>
                </div>
              </div>
            </div>

            {/* Quick Actions & System Status */}
            <div className="space-y-8">
              <div>
                <h2 className="text-lg font-semibold text-textMain mb-4 flex items-center gap-2">
                  <Zap className="w-5 h-5 text-warning" />
                  {t('actions.title')}
                </h2>
                <div className="grid grid-cols-2 gap-3">
                  {['ackAll', 'pause', 'export', 'diagnostics'].map((actionId) => (
                    <button key={actionId} className="glass-panel p-3 rounded-xl text-sm text-textMain hover:text-primary hover:border-primary/50 transition-colors text-left flex flex-col gap-2 group">
                      <Terminal className="w-4 h-4 text-textMuted group-hover:text-primary transition-colors" />
                      {t(`actions.${actionId}`)}
                    </button>
                  ))}
                </div>
              </div>

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
                <div className="space-y-3 max-h-56 overflow-y-auto">
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
          </div>
        </main>
      </div>
    </div>
  );
}

export default App;
