import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';

import zhTranslations from './locales/zh.json';
import enTranslations from './locales/en.json';

// 获取浏览器默认语言
const getBrowserLanguage = () => {
  const lang = navigator.language;
  return lang.toLowerCase().includes('zh') ? 'zh' : 'en';
};

i18n
  .use(initReactI18next)
  .init({
    resources: {
      zh: {
        translation: zhTranslations,
      },
      en: {
        translation: enTranslations,
      },
    },
    // 默认优先使用用户已保存语言，否则根据浏览器语言自动选择
    lng: localStorage.getItem('atlas_lang') || getBrowserLanguage(),
    fallbackLng: 'en',
    interpolation: {
      escapeValue: false, // React 已经自带防 XSS 注入
    },
  });

export default i18n;
